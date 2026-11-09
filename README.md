# q2-pocket

A podcast app for the **Shanling Q2** running [Q2 Pod](https://github.com/DiamondBond/q2-pod), with your **Pocket Casts** account and **no ads**. It does everything [q2-podcasts](https://github.com/m-d-e-v/q2-podcasts) does (subscribe by feed or search, download over Wi-Fi, play from the card's `Podcasts` folder, where Q2 Pod resumes each episode and keeps it out of your music library), and:

- **Pocket Casts sync**, as [pocketcli](https://github.com/foforivera/pocketcli) does it from a computer: your podcasts, Up Next, In Progress, New Releases, Starred, History, uploaded Files and Discover; where you are in each episode, both ways; played episodes marked played; the episode you start becomes Up Next's first and goes in your listening history.
- **Offline:** all of Up Next stays downloaded (or its first few, if you choose), and any episode downloads with a press. Each episode's description goes beside it, and its chapters into it when its publisher gives them, for Q2 Pod's Now Playing.
- **No ads:** every MP3 it downloads has the ads its host stitched in removed before it lands on the card, and sponsor reads in the episode itself are cut too.

It needs Q2 Pod with card apps (1.4.2 or later). Two things want newer Q2 Pod: Up Next playing straight through in order (firmware that lists `playlist` in `/tmp/q2-addon.caps`), and places moved on your phone arriving on the Q2 (`places`). [Q2 Podcast](https://github.com/group-a-study/q2-podcast) has both.

## Install

Download `q2-pocket-*.zip` from the latest release, power the Q2 off, and unzip it at the root of its microSD card: the card then has `.pocket/q2-pocket`, `.pocket/run`, `.pocket/q2app` and the icons. Put the card back and power the Q2 on. **Pocket** is then a row in **Streaming**. Once Q2 Pod's `sources.list` has a line for this repository (`group-a-study/q2-pocket Pocket`), **System settings → Apps** installs and updates it too.

## Signing in

Open **Streaming → Pocket → Sign In to Pocket Casts**: the Q2 shows a QR code and a PIN. Scan it with your phone (same Wi-Fi), open **Settings** and sign in with your Pocket Casts email and password. The password goes to Pocket Casts only; the Q2 keeps the session Pocket Casts gives (`.pocket/account.json`) and renews it. Your podcasts appear on the Q2 within a minute. Shows you had subscribed to by their feed before signing in are found in Pocket Casts and subscribed there too; a private feed Pocket Casts doesn't know stays the Q2's own.

Signed out, Pocket works as q2-podcasts does, ad removal included.

## How places sync

Every three minutes while Pocket runs (and at once with **Sync Now**), each downloaded episode's place on the Q2 and in Pocket Casts are compared with where they were at the last sync. Pocket Casts says nothing about *when* a place changed, so:

- Moved only on the Q2: sent to Pocket Casts. Moved only elsewhere: put in the Q2's resume place, so the episode starts there (a Q2 Pod with `places`).
- Moved on both, or an episode new to the sync with a place on each side: the further place wins on both, so nothing heard is lost and neither side is set back.
- A place of 0 is never sent: starting an episode again near its beginning doesn't wipe Pocket Casts' place.
- Finished on the Q2 (its place gone in the last five minutes): marked played and taken out of Up Next. Marked played elsewhere: the Q2 forgets its place. Marked unplayed elsewhere: the same.
- Places are mapped through the cut ads, so a place on the Q2 and the same moment on your phone agree.

Without `places`, a place from elsewhere waits until the Q2 itself moves on; nothing is sent back over it.

## Use

On the Q2 (**Streaming → Pocket**):

- **Up Next**, **In Progress**, **New Releases**, **Starred**, **History**: press an episode to download it, or, once it is on the Q2, to play it (in Up Next, on through Up Next in order). **Hold Play/Pause** on an episode to add it to the end of Up Next; in Up Next, hold it twice to take an episode out.
- **Podcasts**: your shows; each has its episodes and **Show Options** (update, download new episodes by themselves, delete downloads, unsubscribe, also in Pocket Casts).
- **Downloaded**, **Downloads**, **Files** (Pocket Casts Plus' uploaded files), **Discover** (Pocket Casts' Trending, Popular and Featured), **Search Podcasts**, **Sleep Timer**.
- **Ads & Sponsors**: the switches below, what has been cut so far, how much of Up Next stays downloaded, and whether played episodes are deleted.
- On [Q2 Podcast](https://github.com/group-a-study/q2-podcast), Q2 Pod made for podcasts: Now Playing's heart is the episode's star in Pocket Casts, its **Description** and **Chapters** pages show what Pocket writes beside the episode, and the side buttons skip 15 seconds back and 30 on, or hold for the chapters.
- **Sync Now**: syncs at once; it also syncs every three minutes while Pocket runs, and stays on while a podcast plays so your place keeps reaching Pocket Casts.

On the phone page (the QR code): tabs for all of the above. An episode's sheet plays it next or last, takes it out of Up Next, stars it, marks it played or unplayed, downloads it, and shows **what was cut** from it: each cut's times and why. **Settings** has your account, the switches and the optional keys.

An episode's row says what was cut, "3 ads and 1 sponsor read cut (4 min)".

## How the ads come out

Most podcast hosts put ads in as they serve an episode (dynamic ad insertion): they stitch separately encoded ad files between the episode's own pieces, at MP3 frame boundaries. Q2 Pocket finds them in the MP3's frames, without decoding the audio, so it runs on the Q2 itself:

- **Copies differ only in their ads.** The episode's frames are the same bytes in every copy served; the ads differ from listener to listener. When the host would send another copy with other ads (asked for first with a one-byte request, so nothing is downloaded twice for nothing), Pocket fetches it too: whatever the copies don't share is advertising.
- **An ad is the same bytes wherever it plays.** An episode's own audio is encoded once, as a whole, so it never matches anything byte for byte (silence aside, which is left out). Stretches this episode shares with your recent episodes, or with an ad found before (it keeps the ads it learns, `.pocket/ads.bin`, so a campaign heard once is known in every show), or that play twice in it, are ads.
- **Every stitched piece starts with a fresh encoder.** The encoder's padding pattern starts again, its bit reservoir is empty, and often its header bits differ. That cuts the episode into its parts: a few long ones (the episode, between ad breaks) and short ones (the ads). Short parts between long ones, and after the last, are ads. A short first part is cut only when something confirms it (the checks above, the feed's own length for the episode, or a transcript that opens with a sponsor), since it may be the episode's cold open.

Each cut is made at a frame boundary, and a frame whose data began in a cut frame is made silent (26 ms), so a join never clicks. Chapters move with the audio, and the file's length is rewritten. Tested on ten real episodes from seven hosts (Megaphone, Simplecast and AdsWizz, NPR, iHeart's Omny, Art19, Acast), labelled by transcribing them: 93% of the ad time cut, 2 seconds of the episodes lost, and the cut files decode cleanly at the feeds' own lengths.

**Sponsor reads** recorded into the episode itself aren't stitched in. Pocket looks for them in:

- **SponsorBlock**, the crowd-sourced database: the episode's **YouTube** video, or its **Spotify** episode through **Spot SponsorBlock**. They are found from the episode's show notes, or, with your own key (Settings), with the YouTube Data API or a Spotify app. Their segments are used only when the video or Spotify episode is as long as the episode (its own cut, or as downloaded), so they line up.
- **Transcripts**: the publisher's (Podcasting 2.0) or Pocket Casts' own, read for sponsor reads ("this episode is brought to you by…", then an address or a code, to "back to the show").
- **Chapters** named as ads or sponsors.

Self-promotion (a show's plugs for itself) is left in unless you switch it on.

**Limits.** Only MP3 episodes are cut (nearly all podcasts; an M4A goes on the card as it came). An ad the host bakes into every copy, that no other episode carries and that isn't a separate stitched piece stays until it is learned. Sponsor reads need SponsorBlock, a transcript or chapters to know about them. Comparing copies costs a second download when the host would send other ads.

## Files

Everything besides the app is kept over updates and removal:

- `.pocket/config.json`: the phone page's key and PIN, the switches, `download_up_next`, `delete_played`, `idle_minutes`, and the optional `youtube_api_key`, `spotify_client_id`, `spotify_client_secret`.
- `.pocket/account.json`: the Pocket Casts session (delete it to sign out). `.pocket/sync.json`: each episode's place as last synced.
- `.pocket/subscriptions.opml`: your shows (Pocket Casts ids kept in their own attributes), `.pocket/feeds/`, `.pocket/queue.json`, `.pocket/partial/`.
- `Podcasts/<Show>/.description/`: each episode's description, as text, for Now Playing.
- `.pocket/ads.bin`: the ads learned. `.pocket/frames/`: recent episodes' frames, to compare new ones with. `.pocket/cuts/`: what was cut from each episode and why, which also maps a place in the cut episode to the episode as served, so places sync with your other devices.
- `Podcasts/<Show>/2024-10-07 - Episode Title.mp3`: the episodes. `Podcasts/Pocket Casts Files/`: uploaded files.

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
