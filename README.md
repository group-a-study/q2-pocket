# q2-pocket

A podcast app for the **Shanling Q2** running [Q2 Pod](https://github.com/DiamondBond/q2-pod), with your **Pocket Casts** account and **no ads**. It does everything [q2-podcasts](https://github.com/m-d-e-v/q2-podcasts) does (subscribe by feed or search, download over Wi-Fi, play from the card's `Podcasts` folder, where Q2 Pod resumes each episode and keeps it out of your music library), and:

- **Pocket Casts on the Q2**, as [pocketcli](https://github.com/foforivera/pocketcli) reads it from a computer: your podcasts, Up Next, In Progress, New Releases, Starred, History, uploaded Files and Discover, and where you are in each episode. By default the Q2 only takes your account's changes and sends none back (no places, no played marks, no Up Next changes), except what you ask for in Pocket itself (a star, Add to Up Next, a subscription); **Sync: Two-Way** sends the Q2's places, finished episodes and what it plays too.
- **The Q2's own Up Next:** your account's Up Next, taken in at each sync. An episode added to it on your phone goes to the top of the Q2's (after the episode the Q2 is playing); one taken out on your phone leaves the Q2's; one you finish on the Q2 leaves the Q2's Up Next and stays in your account's; the episode you start goes first.
- **Offline:** the first ten episodes of Up Next stay downloaded (choose how many, or all of it), **Download Next** on the Up Next page fetches the next few (5 unless you choose 1, 3, 10 or 20), **Download All** fetches the rest once, and any episode downloads with a press. An episode you cancel or delete stays off the Q2: Up Next doesn't download it again by itself (a press still does). Each episode's description goes beside it, its chapters into it when its publisher gives them, and its transcript beside it as a timed `.lrc`, for [Q2 Podcast](https://github.com/group-a-study/q2-podcast)'s Now Playing (its Transcript page follows the episode as it plays). Once new episodes are on the card, Pocket asks Q2 Pod to update its library, so they show in its lists.
- **No ads:** every MP3 it downloads has the ads its host stitched in removed before it lands on the card, and sponsor reads in the episode itself are cut too.

It needs Q2 Pod with card apps (1.4.2 or later). Two things want newer Q2 Pod: Up Next playing straight through in order (firmware that lists `playlist` in `/tmp/q2-addon.caps`), and places moved on your phone arriving on the Q2 (`places`). [Q2 Podcast](https://github.com/group-a-study/q2-podcast) has both.

## Install

Download `q2-pocket-*.zip` from the latest release, power the Q2 off, and unzip it at the root of its microSD card: the card then has `.pocket/q2-pocket`, `.pocket/run`, `.pocket/q2app` and the icons. Put the card back and power the Q2 on. **Pocket** is then a row in **Streaming**. Once Q2 Pod's `sources.list` has a line for this repository (`group-a-study/q2-pocket Pocket`), **System settings → Apps** installs and updates it too.

## Signing in

Open **Streaming → Pocket → Sign In to Pocket Casts**: the Q2 shows a QR code and a PIN. Scan it with your phone (same Wi-Fi), open **Settings** and sign in with your Pocket Casts email and password. The password goes to Pocket Casts only; the Q2 keeps the session Pocket Casts gives (`.pocket/account.json`) and renews it. Your podcasts appear on the Q2 within a minute. Shows you had subscribed to by their feed before signing in are found in Pocket Casts and subscribed there too; a private feed Pocket Casts doesn't know stays the Q2's own.

Signed out, Pocket works as q2-podcasts does, ad removal included.

## How places and Up Next come in

Every three minutes while Pocket runs, as you open it (behind the menu, which opens at once) and at once with **Sync Now**, Pocket asks Pocket Casts for your Up Next and places. One-way (the default, **Ads & Sponsors → Sync: One-Way**, or the phone page's **Two-way sync** off), nothing goes back: Pocket Casts keeps what your phone did, the Q2 what it did. Two-way, the Q2's own go back too: a place it saves is sent (a place of 0 never is, so starting an episode again doesn't wipe Pocket Casts'), an episode it finishes is marked played and leaves your account's Up Next, and the episode it starts becomes your account's Up Next first and goes in your listening history.

- A place moved elsewhere (since the last sync) goes in the Q2's resume place, so the episode starts there (a Q2 Pod with `places`); moved on both, the further one wins on the Q2. Places are mapped through the cut ads, so a place on your phone and the same moment on the Q2 agree. Marked played or unplayed elsewhere: the Q2 forgets its place.
- A place moved on the Q2 stays on the Q2 (two-way: it is sent; moved on both, the further one wins on both).
- Up Next: an episode new to your account's Up Next goes first in the Q2's (after the episode the Q2 is playing, when that is first); one taken out of your account's leaves the Q2's; one added again later counts as new again.
- Finished on the Q2: it leaves the Q2's Up Next (and is deleted if **Delete Played Episodes** is on), and your account's Up Next keeps it (two-way: it's marked played and leaves your account's too). [Q2 Podcast](https://github.com/group-a-study/q2-podcast) 1.8.2 or later notes each episode played to its end (`/mnt/data/ringnav-done`), so this counts even when Pocket wasn't running as it ended; on other firmware, its place gone within five minutes of the end counts.

Without `places`, a place from elsewhere waits until the Q2 itself moves on.

## Use

On the Q2 (**Streaming → Pocket**):

- **Up Next**, **In Progress**, **New Releases**, **Starred**, **History**: press an episode to download it, or, once it is on the Q2, to play it (in Up Next, on through Up Next in order). Up Next starts with **Download All**: press it twice to download every episode in Up Next not on the Q2 yet (press it again to stop asking for more). Then **Download Next 5**: press it to download the next five not on the Q2 yet, after those Up Next keeps downloaded; **hold Play/Pause** on it for more or fewer (1, 3, 5, 10, 20). The phone page has both, with a count to pick. **Hold Play/Pause** on an episode to add it to the end of Up Next (on New Releases, to put it first in Up Next, after the one playing); in Up Next, hold it twice to take an episode out.
- **Podcasts**: your shows; each has its episodes and **Show Options** (update, download new episodes by themselves, delete downloads, unsubscribe, also in Pocket Casts).
- **Downloaded**, **Downloads**, **Files** (Pocket Casts Plus' uploaded files), **Discover** (Pocket Casts' Trending, Popular and Featured), **Search Podcasts**, **Sleep Timer**. **Downloads** starts with **Stop After This One**: the download under way finishes, every waiting one is cancelled, and Up Next isn't downloaded by itself again until **Resume Downloads** (or **Download All**).
- **Old episodes download too.** An episode in Up Next that Pocket's copy of its show lacks (older than the show's newest 500, dropped from its feed, or published again under a new id) is looked up in the show's full list on Pocket Casts, by its id or else by its title within a month of its date, and downloaded from the address there, or from the account's own copy of it. Links in front of an episode's address that no longer work are passed over to the address behind them: Chartable's (closed in 2024) and Blubrry's media.rawvoice.com (its certificate is another site's).
- **Downloads follow the Q2's Up Next.** An episode Pocket downloaded because it was in Up Next is cancelled once you take it out of Up Next in Pocket Casts, and after each start nothing downloads until Pocket has synced (two minutes at most), so a removal counts first. An episode Pocket crashes on (its launcher sees the crash) goes after the others and is checked in full again; only a second crash in it, in the same version of Pocket, leaves the ad check out (the part that needs the most memory), so it still lands, as it came, marked **Ads Not Removed**. A new version of Pocket tries every episode afresh; after four crashes it is skipped as failed (**Try Failed Again** on Downloads, or **Download Again** on its sheet on the phone page, starts it over); stopping Pocket or switching the Q2 off mid-download doesn't count. A fault while handling one episode fails only that episode.
- Signed in, Pocket takes Pocket Casts' changes each time you open it (Q2 Podcast says when its page opens; on Q2 Pod, an opening after 45 s away), behind the menu: it opens at once, without waiting.
- **Ads & Sponsors**: what comes out, always (ads, sponsor reads, self-promotion, with a second copy compared: none of it can be turned off), what has been cut so far, how much of Up Next stays downloaded (the first 10 unless you choose otherwise: Off, 3, 5, 10, 20, 50 or all of it), how many Download Next asks for, whether played episodes are deleted, and **Sync: One-Way** or **Two-Way**.
- On [Q2 Podcast](https://github.com/group-a-study/q2-podcast), Q2 Pod made for podcasts: Now Playing's heart is the episode's star in Pocket Casts, its **Description** and **Chapters** pages show what Pocket writes beside the episode, and the side buttons skip 15 seconds back and 30 on, or hold for the chapters.
- **Sync Now**: takes Pocket Casts' changes at once; it also does every three minutes while Pocket runs.

On the phone page (the QR code): tabs for all of the above. Up Next and the lists come in pages of 50 with filters (**On the Q2**, **Downloading**, **Failed**, **Ads Not Removed**, **Not on the Q2**), and Up Next opens with the download under way and its progress, however far down Up Next it is. **On the Q2** lists every downloaded episode; its **Ads Not Removed** filter has **Remove Ads Again for All**. An episode's sheet plays it next or last, takes it out of Up Next, stars it, marks it played or unplayed, downloads it, shows **what was cut** from it (each cut's times and why), and, for one on the Q2, **Remove Ads Again**: it downloads the episode again and checks it anew, the one on the Q2 playing until the new one takes its place (its row shows the download's progress meanwhile); if it is playing then, Q2 Podcast goes on from the same moment of the new copy (also on the Q2: **Ads & Sponsors → Remove Ads Again**, press an episode twice). **Settings** has your account, the settings and the optional keys.

An episode's row says what was cut, "3 ads and 1 sponsor read cut (4 min)".

## How the ads come out

Most podcast hosts put ads in as they serve an episode (dynamic ad insertion): they stitch separately encoded ad files between the episode's own pieces, at MP3 frame boundaries. Q2 Pocket finds them in the MP3's frames, without decoding the audio, so it runs on the Q2 itself:

- **Copies differ only in their ads.** The episode's frames are the same bytes in every copy served; the ads differ from listener to listener. When the host would send another copy with other ads (asked for first with a one-byte request, so nothing is downloaded twice for nothing), Pocket fetches it too: whatever the copies don't share is advertising.
- **An ad is the same bytes wherever it plays.** An episode's own audio is encoded once, as a whole, so it never matches anything byte for byte (silence aside, which is left out). Stretches this episode shares with your recent episodes, or with an ad found before (it keeps the ads it learns, `.pocket/ads.bin`, so a campaign heard once is known in every show), or that play twice in it, are ads.
- **Every stitched piece starts with a fresh encoder.** The encoder's padding pattern starts again, its bit reservoir is empty, and often its header bits differ. That cuts the episode into its parts: a few long ones (the episode, between ad breaks) and short ones (the ads). Short parts between long ones, and after the last, are ads. A short first part is cut only when something confirms it (the checks above, the feed's own length for the episode, or a transcript that opens with a sponsor), since it may be the episode's cold open.

Each cut is made at a frame boundary, and a frame whose data began in a cut frame is made silent (26 ms), so a join never clicks. Chapters move with the audio, and the file's length is rewritten. Tested on ten real episodes from seven hosts (Megaphone, Simplecast and AdsWizz, NPR, iHeart's Omny, Art19, Acast), labelled by transcribing them: 93% of the ad time cut, 2 seconds of the episodes lost, and the cut files decode cleanly at the feeds' own lengths.

**Sponsor reads** recorded into the episode itself aren't stitched in. Pocket looks for them in:

- **SponsorBlock**, the crowd-sourced database: the episode's **YouTube** video, or its **Spotify** episode through **Spot SponsorBlock**. They are found from the episode's show notes, or, with your own key (Settings), with the YouTube Data API or a Spotify app. Their segments are used only when the video or Spotify episode is as long as the episode (its own cut, or as downloaded), so they line up.
- **Transcripts**: the publisher's (Podcasting 2.0) or Pocket Casts' own, read for sponsor reads ("this episode is brought to you by…", then an address or a code, to "back to the show"). Pocket Casts makes its transcript from the copy its own server was sent, with that copy's adverts, so it is often longer or shorter than the download. Pocket lines such a transcript up through the download's ad slots (adverts it cut, and the moments of digital silence a host leaves a slot as when it has no advert for you): a read that starts at a slot was the other copy's advert; a read anywhere else is in the episode itself, and is cut. The transcript beside the episode on the Q2 follows the same lining up.
- **Chapters** named as ads or sponsors.
- **Show notes' timestamps** naming a sponsor ("(00:17:35) Sponsors: AG1", as Huberman Lab's notes list them): each such part, to the next timestamp, on the episode as its host made it (its stitched ads cut first). A list counts with three timestamps or more, rising, the first in its first minute.

Self-promotion (a show's plugs for itself) comes out too, where SponsorBlock marks it.

**Memory.** The Q2 has little memory to spare. The check holds 20 bytes per MP3 frame, frame hashes at 32 bits (the episode's, a second copy's, the episodes it compares with, one at a time), and the library of learned adverts as one row of 4 bytes a frame: a three-hour episode with a full library takes about 18 MB (v1.10 took about 35 MB, and the library alone 15 MB, which ended Pocket again and again during a long Download All). Show notes are read for the one episode, not kept for the whole show. While an episode is checked, syncs and feed reads wait, so the memory the check measured free stays free.

**A whole check or none.** Every episode is checked with every source of evidence. Before a check, Pocket works out what it needs (the episode's frames, the library, the longest episode it compares with) and waits up to 30 seconds for that much to be free. If it isn't, the episode goes on the Q2 as it came, marked **Ads Not Removed** with the reason, never half-checked. Those episodes (and any Pocket crashed checking) are listed under **Ads & Sponsors → Ads Not Removed** on the Q2 and the **Ads Not Removed** filters on the phone page, ready for **Remove Ads Again**, one at a time or all together.

**Empty ad slots.** Where the host had no advert for you, it leaves a moment of silence (Megaphone: a second and a half). When the two copies differ only there, that silence is cut, and the report calls it an empty ad slot, not an ad.

**Room.** An episode is cut and tagged into a copy beside the download, which then takes its place; on a card without room for that copy, it is cut and tagged in the download itself, to the same bytes (one stopped part way through that is downloaded again). Before, such an episode went on the card as it came, its ads and its publisher's own title in.

**Limits.** Only MP3 episodes are cut (nearly all podcasts; an M4A goes on the card as it came). An ad the host bakes into every copy, that no other episode carries and that isn't a separate stitched piece stays until it is learned. Sponsor reads need SponsorBlock, a transcript, chapters or show notes' timestamps to know about them. Comparing copies costs a second download when the host would send other ads.

## Files

Everything besides the app is kept over updates and removal:

- `.pocket/config.json`: the phone page's key and PIN, `download_up_next` (10 unless chosen; -1 all), `delete_played`, `idle_minutes`, and the optional `youtube_api_key`, `spotify_client_id`, `spotify_client_secret`.
- `.pocket/account.json`: the Pocket Casts session (delete it to sign out). `.pocket/sync.json`: each episode's place as last synced, and the Q2's Up Next (its order, what your account's had, those finished on the Q2).
- `.pocket/subscriptions.opml`: your shows (Pocket Casts ids kept in their own attributes), `.pocket/feeds/`, `.pocket/queue.json`, `.pocket/partial/`. `.pocket/changes`: how many times episodes on the card changed in place (retagged, cut again), so Q2 Pod's library reads them again.
- `Podcasts/<Show>/.description/`: each episode's description, as text, for Now Playing.
- `.pocket/ads.bin`: the ads learned. `.pocket/frames/`: recent episodes' frames, to compare new ones with. `.pocket/cuts/`: what was cut from each episode and why, which also maps a place in the cut episode to the episode as served, so places sync with your other devices.
- `Podcasts/<Show>/2024-10-07 - Episode Title.mp3`: the episodes, tagged with the episode's title as Pocket Casts has it (a publisher's own tag can name it otherwise: an Acast episode's said "THEME SONG", from the theme song stitched on its front) and a JPEG cover (Q2 Podcast's lists show no PNG). Episodes already on the card are tagged so in the background, in place when the new tag fits. `Podcasts/Pocket Casts Files/`: uploaded files.
- `q2-pocket.log` at the card's root (`q2-pocket.old.log` before it, 512 KB each): the debug log. Each start, with what the run before left (a crash's message, the launcher's restarts and exit codes); each sync and why one failed; each download, its steps with the memory then, and how it ended; memory every minute; and why Pocket stopped. Send it along when something goes wrong.

## Build

    contrib/shanlingq2/build.sh

Go 1.22 or later and python3; no dependencies outside Go's standard library. Tests:

    go test ./...
    python3 contrib/shanlingq2/test_launcher.py
    Q2POCKET_DAI=dir go test -tags realaudio -run TestReal -v ./internal/adfree   # labelled real episodes
    go test -tags live -run TestLive -v ./internal/pocketcasts                    # Pocket Casts' public API

Pushing a tag `q2-vX.Y` that matches `cmd/q2-pocket/main.go`'s version builds and publishes the release.

## License

[MIT](LICENSE). It starts from [q2-podcasts](https://github.com/m-d-e-v/q2-podcasts) (MIT); `internal/q2`, `internal/q2spot`, `internal/qr`, `package.py` and the launcher follow q2-filebrowser (MIT); the QR encoder follows Project Nayuki's (MIT); `internal/tlsroots/cacert.pem` is Mozilla's CA bundle (MPL-2.0). Pocket Casts is talked to as its web player does, after pocketcli (MIT). Sponsor segments come from [SponsorBlock](https://sponsor.ajay.app)'s database (CC BY-NC-SA 4.0), Spot SponsorBlock's among them. Pocket Casts is Automattic's; this app isn't theirs.
