// q2-podcasts is a Q2 Pod card app (contrib/shanlingq2/README): podcasts from their RSS feeds on
// the Shanling Q2. Subscribe by searching on the Q2, by typing or pasting a feed's address (on the
// Q2, or from a phone through the QR code it shows), or by importing OPML from another podcast app;
// browse each show's episodes and download them into the card's Podcasts folder, where Q2 Pod plays
// them, resumes each where it stopped and keeps each show's playback speed. It runs from the card's
// .podcasts folder (copied to /tmp first, so the card is never kept busy) and draws the Q2's
// Podcasts page over Q2SPOT/1.
package main

import (
	"context"
	"flag"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"runtime/debug"
	"sync"
	"syscall"
	"time"

	"q2-pocket/internal/clean"
	"q2-pocket/internal/feed"
	"q2-pocket/internal/library"
	"q2-pocket/internal/pocketcasts"
	"q2-pocket/internal/q2"
	"q2-pocket/internal/q2spot"
	"q2-pocket/internal/qr"
	"q2-pocket/internal/tlsroots"
)

const version = "q2-v1.1"

const (
	staleAfter   = 30 * time.Minute // a show's feed older than this is fetched again when Podcasts starts
	refreshEvery = time.Hour        // and while it runs
	webHold      = 5 * time.Minute  // the phone's page used this recently keeps the Q2 awake
)

type options struct {
	dir, card, statusFile, browse, control string
	resume                                 string // Q2 Pod's places in what it played (resume.go)
	sleep                                  string // the Sleep Timer's request to Q2 Pod (sleep.go)
	places, play                           string // places for Q2 Pod's resume ring (sync.go), and Up Next to play (pages.go)
	star, starred                          string // Now Playing's star asked for, and the starred episodes (sync.go)
	library                                string // Q2 Pod's library update, asked for (rescan)
	port                                   int
	ready                                  func() bool // downloads may run; tests replace the Wi-Fi and card check
}

type app struct {
	opts   options
	lib    *library.Library
	pc     *pocketcasts.Client
	sy     *syncer
	state  *q2.State
	client *http.Client // the directory's searches
	web    *http.Client // uploaded files' downloads
	port   int
	ln     net.Listener
	ctx    context.Context // background work, ended by shutdown
	cancel context.CancelFunc

	mu       sync.Mutex
	set      settings
	used     time.Time // the Q2's page or the phone's, last
	webAt    time.Time // the phone's, last
	ip       string
	qrFor    string
	qrHex    string
	qrSize   int
	armed    map[string]time.Time // a press that asks for a second one to confirm
	found    map[string]string    // a search result's key: what to fetch for its preview
	loads    map[string]*load     // previews being fetched or fetched, by what was asked
	rescanAt time.Time            // the episodes on the card last compared (rescan)
	scanned  string               // and what they were: their count and bytes
	placeMap map[uint32]int       // Q2 Pod's resume ring as last read (places), and its file's time
	placesAt time.Time
	sleepSet int // the Sleep Timer's minutes as last chosen, for its page's Selected

	pending  sync.Mutex          // the pages' own: getting, lists, fileJobs
	getting  map[string]bool     // Pocket Casts episodes whose show is being added to download them
	lists    map[string]cached   // Discover's lists, by name
	fileJobs map[string]*fileJob // uploaded files downloading
	fileMu   sync.Mutex          // one file at a time
	quit     chan struct{}
	once     sync.Once
}

func main() {
	var o options
	flag.StringVar(&o.dir, "dir", "/mnt/mmc/.pocket", "the app's folder on the card")
	flag.StringVar(&o.card, "card", "/mnt/mmc", "the card's mount point")
	flag.StringVar(&o.statusFile, "status-file", "/tmp/q2-pocket.state", "the status file Q2 Pod reads")
	flag.StringVar(&o.resume, "resume-file", "/mnt/data/ringnav-resume", "Q2 Pod's places in what it played")
	flag.StringVar(&o.sleep, "sleep-file", "/tmp/q2-pocket.sleep", "the Sleep Timer's request to Q2 Pod")
	flag.StringVar(&o.places, "places-file", "/tmp/q2-pocket.places", "places for Q2 Pod's resume ring")
	flag.StringVar(&o.play, "play-file", "/tmp/q2-pocket.play", "a playlist for Q2 Pod to play")
	flag.StringVar(&o.star, "star-file", "/tmp/q2-star", "Now Playing's star, asked for")
	flag.StringVar(&o.starred, "starred-file", "/tmp/q2-starred", "the downloaded episodes starred")
	flag.StringVar(&o.library, "library-file", "/tmp/q2-pocket.library", "Q2 Pod's library update, asked for")
	flag.StringVar(&o.browse, "browse-socket", "/tmp/q2-pocket.browse.sock", "Q2SPOT/1 for the Q2's page")
	flag.StringVar(&o.control, "control-socket", "/tmp/q2-pocket.sock", "datagrams: q quits")
	flag.IntVar(&o.port, "port", 8100, "the phone page's port (the next free one if taken)")
	flag.Parse()
	debug.SetGCPercent(50)
	debug.SetMemoryLimit(48 << 20)
	// q2.Card stays the Q2's mount point (/mnt/mmc), whatever -card says: its writes check that the
	// card is mounted there, which a folder given for a run on a computer never is.
	a, err := start(o)
	if err != nil {
		os.Exit(1) // the launcher tries again
	}
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGTERM, syscall.SIGINT, syscall.SIGHUP)
	go func() { <-sig; a.shutdown() }()
	a.run()
	os.Exit(0)
}

// touch notes a use: web is the phone's (which holds standby off a while).
func (a *app) touch(web bool) {
	a.mu.Lock()
	a.used = time.Now()
	if web {
		a.webAt = a.used
	}
	a.mu.Unlock()
}

// listen takes port, or the next free of the nine after it.
func listen(port int) (net.Listener, int) {
	for p := port; p < port+10; p++ {
		if l, err := net.Listen("tcp4", fmt.Sprintf(":%d", p)); err == nil {
			return l, p
		}
	}
	return nil, 0
}

// importCard takes podcasts.opml from the card's root, exported from another app and copied there
// by hand, then renames it so it is taken once.
func importCard(lib *library.Library, card string) {
	path := filepath.Join(card, "podcasts.opml")
	f, err := os.Open(path)
	if err != nil {
		return
	}
	outlines, err := feed.ParseOPML(f)
	f.Close()
	if err != nil {
		return
	}
	lib.Import(outlines)
	os.Rename(path, path+".imported")
}

func start(o options) (*app, error) {
	if err := os.MkdirAll(o.dir, 0o755); err != nil {
		return nil, err
	}
	feeds := tlsroots.Transport()
	agent := "Q2Pocket/" + version[len("q2-v"):] + " (Shanling Q2; Q2 Pod card app)" // hosts count listens by it
	set := loadSettings(o.dir)
	pc := pocketcasts.New(&http.Client{Transport: feeds, Timeout: 60 * time.Second}, agent, loadAccount(o.dir))
	pc.Saved = func(t pocketcasts.Token) { saveAccount(o.dir, t) }
	var a *app
	cl := &clean.Cleaner{Dir: o.dir, Fetch: &http.Client{Transport: tlsroots.Transport(), Timeout: 30 * time.Minute},
		API: &http.Client{Transport: feeds, Timeout: 60 * time.Second}, Agent: agent, PC: pc,
		Settings: func() clean.Settings { return a.cleanSettings() }}
	lib, err := library.Open(library.Options{
		Dir:    o.dir,
		Root:   filepath.Join(o.card, "Podcasts"),
		Client: &http.Client{Transport: feeds, Timeout: 60 * time.Second},
		Fetch:  &http.Client{Transport: tlsroots.Transport()},
		Agent:  agent,
		Ready:  o.ready,
		PC:     pc,
		Clean:  cl,
	})
	if err != nil {
		return nil, err
	}
	importCard(lib, o.card)
	ctx, cancel := context.WithCancel(context.Background())
	// Turned on counts as used: the Q2 stays awake while the phone scans the code.
	a = &app{opts: o, lib: lib, pc: pc, set: set, state: &q2.State{Path: o.statusFile}, ctx: ctx, cancel: cancel,
		client: &http.Client{Transport: feeds, Timeout: 10 * time.Second}, used: time.Now(),
		web:   &http.Client{Transport: tlsroots.Transport()},
		armed: map[string]time.Time{}, found: map[string]string{}, loads: map[string]*load{},
		quit: make(chan struct{})}
	a.sy = newSyncer(a)
	if a.ln, a.port = listen(o.port); a.ln == nil {
		return nil, fmt.Errorf("no port for the phone's page from %d", o.port)
	}
	go serveWeb(a, a.ln)
	a.writeState()
	go q2spot.Serve(o.browse, a.device)
	q2.Control(o.control, func(cmd []byte) {
		if len(cmd) > 0 && cmd[0] == 'q' {
			a.shutdown()
		}
	})
	lib.DL.Start()
	go a.sy.run(ctx)
	go func() {
		lib.Retag(ctx) // episodes an older version left untagged, with the art the feeds had
		for first := true; ; first = false {
			lib.RefreshAll(ctx, staleAfter)
			if first { // a cover the feed's fresh art address gives now
				lib.Retag(ctx)
			}
			select {
			case <-ctx.Done():
				return
			case <-time.After(refreshEvery):
			}
		}
	}()
	return a, nil
}

func (a *app) shutdown() { a.once.Do(func() { close(a.quit) }) }

// run writes the status file every second and stops by itself once unused for IdleMinutes with
// nothing to download.
func (a *app) run() {
	t := time.NewTicker(time.Second)
	defer t.Stop()
	for {
		select {
		case <-a.quit:
			a.cancel()
			a.lib.DL.Stop() // the episode under way kept, to resume next time
			a.ln.Close()
			a.state.Remove()
			os.Remove(a.opts.browse)
			os.Remove(a.opts.control)
			return
		case <-t.C:
			a.writeState()
			a.sy.star()
			if time.Since(a.rescanAt) >= rescanEvery {
				a.rescanAt = time.Now()
				a.rescan()
			}
			s := a.settings()
			if a.sy.listening() { // a podcast playing: its place is synced, so stay
				a.touch(false)
			}
			a.mu.Lock()
			idle := time.Since(a.used)
			a.mu.Unlock()
			if s.IdleMinutes > 0 && idle > time.Duration(s.IdleMinutes)*time.Minute && !a.working() {
				a.shutdown()
			}
		}
	}
}

// rescanEvery is how often the episodes on the card are compared with those Q2 Pod's library was
// last asked to take in.
const rescanEvery = time.Minute

// rescan asks Q2 Pod to update its library (Update Local Music, which it runs once nothing plays)
// when the episodes on the card have changed and no download is under way: Q2 Podcast's Library
// lists Episodes, Starred and Recently Played from it. A Q2 Pod without card apps' library updates
// (caps "library") is left alone.
func (a *app) rescan() {
	if !q2.HasCap("library") {
		return
	}
	for _, j := range a.lib.DL.Jobs() {
		if j.State != library.Failed {
			return
		}
	}
	eps, bytes := a.lib.Downloaded()
	mark := fmt.Sprintf("%d %d", len(eps), bytes)
	if mark == a.scanned {
		return
	}
	kept := filepath.Join(a.opts.dir, "scanned") // the mark, kept across starts
	if a.scanned == "" {
		if b, err := os.ReadFile(kept); err == nil {
			a.scanned = string(b)
		}
		if mark == a.scanned {
			return
		}
	}
	req := fmt.Sprintf("%d\nscan\n", time.Now().UnixMilli())
	if q2.WriteFile(a.opts.library, []byte(req)) == nil {
		a.scanned = mark
		q2.WriteFile(kept, []byte(mark))
	}
}

// online is whether the Q2 has Wi-Fi (tests say).
func (a *app) online() bool {
	if a.opts.ready != nil {
		return a.opts.ready()
	}
	return q2.LocalIP() != ""
}

// working is whether something runs that standby would break: a feed being fetched, or a download
// that can go ahead (Wi-Fi up).
func (a *app) working() bool {
	return a.lib.Refreshing() > 0 || a.lib.Retagging() || a.lib.DL.Busy() || a.lib.DL.Pending() && q2.LocalIP() != ""
}

// URL is the phone page's address, and with the key the one the QR code opens (signed in at
// once); "" without Wi-Fi.
func (a *app) URL(withKey bool) string {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.ip == "" {
		return ""
	}
	u := fmt.Sprintf("http://%s:%d/", a.ip, a.port)
	if withKey {
		u += "?k=" + a.set.Key
	}
	return u
}

// addr is the address as the Q2 shows it: no scheme, no slash.
func (a *app) addr() string {
	u := a.URL(false)
	if u == "" {
		return ""
	}
	return u[len("http://") : len(u)-1]
}

// writeState is what Q2 Pod reads: the phone page's address, PIN and QR code (addon.c's qr_open,
// from the Add from Your Phone row), and whether to hold standby off.
func (a *app) writeState() {
	ip := q2.LocalIP()
	a.mu.Lock()
	a.ip = ip
	a.mu.Unlock()
	url := a.URL(true)
	a.mu.Lock()
	if url != a.qrFor {
		a.qrFor, a.qrHex, a.qrSize = url, "", 0
		if url != "" {
			if c, err := qr.Encode([]byte(url)); err == nil {
				a.qrHex, a.qrSize = c.Hex(), c.Size
			}
		}
	}
	qrHex, qrSize, pin, web := a.qrHex, a.qrSize, a.set.PIN, time.Since(a.webAt) < webHold
	a.mu.Unlock()
	note := "Add and manage podcasts there"
	if ip == "" {
		note = "Connect the Q2 to Wi-Fi"
	}
	hold := "0"
	if web || a.working() {
		hold = "1"
	}
	a.state.Write(map[string]string{"addr": a.addr(), "pin": pin, "qr": qrHex, "qrsize": fmt.Sprint(qrSize),
		"note": note, "hold": hold, "version": version})
}
