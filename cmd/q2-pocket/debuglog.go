package main

import (
	"fmt"
	"os"
	"runtime"
	"runtime/debug"
	"strings"
	"sync"
	"syscall"
	"time"

	"q2-pocket/internal/library"
	"q2-pocket/internal/q2"
)

// The debug log, which the user asked for to find why Pocket stopped (2026-11-12): q2-pocket.log at
// the card's root, where File Browser shows it. A line each for a start (and what the run before it
// left in /tmp: a crash's message, the launcher's restarts and exit codes), each sync and its
// failure, each download, its steps (the ad check with its sizes) and how it ended, memory every
// minute, and why it stopped. Each line is appended and synced, the file opened and closed around
// it, so it never keeps the card busy; past logMax it starts again, the last one kept as
// q2-pocket.old.log. The launcher (contrib/shanlingq2/launcher.sh) writes its own lines there.

// logPath is the log's file, "" for none (tests).
var logPath = ""

const logMax = 512 << 10

var (
	logMu   sync.Mutex
	started = time.Now()
)

func logf(format string, args ...any) {
	if logPath == "" {
		return
	}
	line := time.Now().Format("2006-01-02 15:04:05.000 ") + fmt.Sprintf(format, args...) + "\n"
	logMu.Lock()
	defer logMu.Unlock()
	if !q2.CardMounted() {
		return
	}
	if fi, err := os.Stat(logPath); err == nil && fi.Size()+int64(len(line)) > logMax {
		os.Rename(logPath, strings.TrimSuffix(logPath, ".log")+".old.log")
	}
	f, err := os.OpenFile(logPath, os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0o644)
	if err != nil {
		return
	}
	f.WriteString(line)
	f.Sync()
	f.Close()
}

// logMem is logf with the memory at the end: the ad check's lines, where it counts.
func logMem(format string, args ...any) { logf(format+"; %s", append(args, memNow())...) }

// errFile is where the launcher puts the app's stderr (a crash's message) and its restarts.
const errFile = "/tmp/q2-pocket.err"

// free is a file system's free space, in MB.
func free(path string) string {
	var st syscall.Statfs_t
	if syscall.Statfs(path, &st) != nil {
		return "?"
	}
	return fmt.Sprintf("%d MB", uint64(st.Bavail)*uint64(st.Bsize)>>20)
}

// memNow is the app's heap and memory from the system, and the system's free memory.
func memNow() string {
	var m runtime.MemStats
	runtime.ReadMemStats(&m)
	return fmt.Sprintf("app heap %d MB, app system %d MB, system free %d MB", m.HeapAlloc>>20, m.Sys>>20,
		q2.MemAvailable()>>20)
}

// memFree is the memory free for an ad check: the system's, after the app has handed back what it
// isn't using.
func memFree() int64 {
	debug.FreeOSMemory()
	return q2.MemAvailable()
}

// queued is the downloads waiting and failed.
func queued(a *app) (waiting, failed int) {
	for _, j := range a.lib.DL.Jobs() {
		if j.State == library.Failed {
			failed++
		} else {
			waiting++
		}
	}
	return
}

// takeErrFile is what the run before this start left in errFile (its last 8 KB), emptied.
func takeErrFile() string {
	b, err := os.ReadFile(errFile)
	if err != nil || len(b) == 0 {
		return ""
	}
	if len(b) > 8<<10 {
		b = b[len(b)-8<<10:]
	}
	os.Truncate(errFile, 0)
	return strings.TrimSpace(string(b))
}

// crashedBefore is whether the run before ended in a crash: its launcher saw it fail (a Go fault, or
// the kernel ending it for want of memory, 137). A run stopped from the Q2 leaves no such line, and
// one the Q2 was switched off in leaves no errFile.
func crashedBefore(before string) bool {
	return strings.Contains(before, "launcher: the app exited with code")
}

// logStart notes a start: what the run before it left, the card's and /tmp's free space, the queue.
func logStart(a *app, before string) {
	if before != "" {
		for _, l := range strings.Split(before, "\n") {
			logf("before this start: %s", l)
		}
	}
	waiting, failed := queued(a)
	logf("start %s (pid %d): card free %s, /tmp free %s, %s; %d downloads waiting, %d failed; caps %q",
		version, os.Getpid(), free(q2.Card), free("/tmp"), memNow(), waiting, failed, caps())
}

// caps is the firmware's card-app features, as it lists them.
func caps() string {
	b, _ := os.ReadFile(q2.CapsFile)
	return strings.TrimSpace(string(b))
}

// logMemory notes the memory and /tmp, every minute.
func logMemory(a *app) {
	waiting, _ := queued(a)
	logf("memory: %s, %d goroutines; /tmp free %s; %d downloads waiting; up %v",
		memNow(), runtime.NumGoroutine(), free("/tmp"), waiting, time.Since(started).Round(time.Second))
}
