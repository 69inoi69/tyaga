# ТЯГА (TYAGA)

Downloads video and audio from YouTube. One program, no install, the interface is a page in your browser. Under the hood it's [yt-dlp](https://github.com/yt-dlp/yt-dlp).

## What it does

- **Video** in MP4 at the best quality. The "for editing" checkbox gives H.264 + AAC, so the file opens in DaVinci Resolve and any player.
- **Audio** in M4A (the original YouTube track, no re-encoding) or MP3 (highest quality).
- For music: clean tags (artist, title without "(Official Video)", year) and a square album cover.
- A whole album as one video with chapters → **split into separate tracks** with numbers and titles.
- **Whole playlists** — into their own folder, numbered.
- A queue: paste a bunch of links at once, cancel any of them.
- **From your phone** over home Wi-Fi: add a link and grab the finished file.

Files are saved to `Downloads/YouTube`.

## How to run

Download the archive for your system from [Releases](../../releases), unpack it anywhere and run `tyaga`. A page opens in your browser.

On the first start TYAGA downloads everything it needs into a `bin` folder next to itself: yt-dlp, ffmpeg and Deno (yt-dlp needs Deno to deal with YouTube). On Windows that's about 250 MB, once. TYAGA updates its own copy of yt-dlp on every start — YouTube changes things often, and old versions stop working. If you already have yt-dlp, ffmpeg or Deno installed, TYAGA uses them and downloads nothing.

The TYAGA folder is portable — move it anywhere, even to a USB stick.

Close the console window and TYAGA quits.

**Windows.** If SmartScreen says "Windows protected your PC", click "More info" → "Run anyway". The program isn't signed, which is normal for small projects.

**macOS.** If the system refuses to open it: `xattr -d com.apple.quarantine tyaga`. Install ffmpeg with Homebrew: `brew install ffmpeg`.

**Linux.** Install ffmpeg from your package manager: `sudo apt install ffmpeg` (or your distro's equivalent).

## From your phone

At the bottom of the page there's "Phone: off". Click it and an address with a short code appears, like `http://192.168.1.5:8765/?t=k7m2pq`. Open it on a phone on the same Wi-Fi. Links added from the phone download to the computer, and finished files can be downloaded right to the phone.

- The code is new every time you turn this on. Without it nobody on the network can do anything, and after 20 wrong attempts the access turns itself off.
- The first time, Windows asks about the firewall — allow access on private networks.
- The computer has to be on: it's the one doing the downloading.

## If YouTube asks you to sign in

Some videos are only available to signed-in users (or YouTube starts thinking you're a bot). Export your YouTube cookies from the browser in Netscape format (for example with the "Get cookies.txt LOCALLY" extension) and put the file as `youtube-cookies.txt` next to the program.

Cookies are access to your account. Don't send them to anyone and don't commit them anywhere (`.gitignore` already excludes them).

## How it works

TYAGA starts a tiny web server on your computer (reachable only from this computer, with a one-time token in the address) and opens the page. The page sends links to the server, the server runs yt-dlp for them one at a time, reads its output and tells the page how it's going.

```
main.go         server, phone access
jobs.go         download queue, yt-dlp command, tags for split tracks
tools.go        finding and auto-downloading yt-dlp, ffmpeg, Deno
proc_*.go       things that differ between Windows and Linux/macOS
index.html      the whole page: markup, styles, script
```

No third-party libraries — only the Go standard library.

## Build it yourself

You need [Go](https://go.dev/dl/) 1.22 or newer.

```
go build -o tyaga .
```

For another system: `GOOS=windows GOARCH=amd64 go build -o tyaga.exe .`

Options: `-out folder` — where to save, `-no-browser` — don't open the browser.

A release with builds for all systems: Actions → Release → Run workflow → a version like `v1.0.0`.

## History

It started as `TYAGA.cmd` — a batch file and a TypeScript program in one file, Windows-only and needing Deno installed. Then it was rewritten in Go to become one program that runs everywhere.

## License

[MIT](LICENSE). yt-dlp, ffmpeg and Deno are separate projects with their own licenses; TYAGA doesn't include them, it downloads them from their official pages.
