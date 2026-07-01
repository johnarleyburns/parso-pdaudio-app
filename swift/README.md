# ParsoKit

A small, dependency-free Swift package for consuming a **parso** public-domain
classical-music library published to Cloudflare R2. It lets you:

- open a distribution SQLite database (downloaded from R2, a local file, or a
  bundled app resource),
- **browse** the work tree (source → composer → work → movement),
- **search** tracks (FTS5), and
- resolve a **streamable audio URL** for each track.

ParsoKit deliberately does **not** include an audio player. It vends a
`ParsoAudioAsset` (a URL + content type) that you hand to AVFoundation. The audio
is Opus-in-CAF (`audio/x-caf`), which AVFoundation decodes natively on Apple
platforms.

Requires: iOS 15+, macOS 12+, tvOS 15+. SQLite is provided by the Apple SDKs (no
third-party dependency).

## Install (SwiftPM)

```swift
.package(url: "https://github.com/johnarleyburns/parso-pdaudio", from: "0.1.0")
// then add "ParsoKit" to your target's dependencies
```

(While it lives in the monorepo `swift/` subdirectory, point SwiftPM at the tag
that contains `swift/Package.swift`, or split it into its own repo for release.)

## Quick start

```swift
import ParsoKit
import AVFoundation

// 1. Resolve audio against your public R2 bucket (or supply your own resolver
//    that returns presigned URLs).
let resolver = PublicBucketResolver(baseURL: URL(string: "https://pub-xxxx.r2.dev")!)

// 2. Open the library — download the DB from R2 once, then cache it locally.
let dbURL = URL.cachesDirectory.appending(path: "parso/library.db")
let library = try await ParsoLibrary.download(
    from: URL(string: "https://pub-xxxx.r2.dev/db/library.db")!,
    to: dbURL,
    audioResolver: resolver)

// (or open a file you already have)
// let library = try ParsoLibrary(path: dbURL, audioResolver: resolver)

// 3. Browse.
for source in try library.sources() {
    print(source.name, source.count)
}
let works = try library.works(source: "chopin", composer: "Frédéric Chopin")
let movements = try library.tracks(workID: works.first!.id)

// 4. Search.
let hits = try library.search("beethoven symphony 5")

// 5. Play with AVFoundation (ParsoKit does not play audio).
if let asset = library.audioAsset(for: hits[0]) {
    let item = AVPlayerItem(url: asset.url)      // asset.contentType == "audio/x-caf"
    let player = AVPlayer(playerItem: item)
    player.play()
}
```

## Embedding in an app (step by step)

This walks through a complete integration: choosing where the database lives,
wiring browse/search into SwiftUI, playing audio with AVFoundation, enabling
background/lock-screen playback, and caching.

### 1. Add the dependency

In Xcode: **File ▸ Add Package Dependencies…** and enter the repository URL, or in
a `Package.swift`:

```swift
dependencies: [
    .package(url: "https://github.com/johnarleyburns/parso-pdaudio", from: "0.1.0"),
],
targets: [
    .target(name: "MyApp", dependencies: [
        .product(name: "ParsoKit", package: "parso-pdaudio")
    ])
]
```

### 2. Decide where the database comes from

| Strategy | When to use | How |
|----------|-------------|-----|
| **Download from R2** | Content updates independently of app releases | `ParsoLibrary.download(from:to:)` on first launch; re-download when you ship new content |
| **Bundled resource** | Ship a fixed catalog inside the app | Add `library.db` to your target, open `Bundle.main.url(forResource:"library", withExtension:"db")!` |
| **Local file** | You manage the file yourself (e.g. App Group, iCloud) | `ParsoLibrary(path:)` |

Downloaded/bundled databases are opened **read-only**, so a bundled DB works
without copying it out of the app bundle.

### 3. Create a shared library service

Open the library once and share it (e.g. via an `@Observable`/`ObservableObject`).
Point the resolver at your bucket. Use a public base URL for a public bucket, or
implement `ParsoAudioResolver` for presigned URLs (see below).

```swift
import ParsoKit

@MainActor
final class LibraryStore: ObservableObject {
    @Published var sources: [ParsoBrowseEntry] = []
    private(set) var library: ParsoLibrary?

    func load() async {
        do {
            let base = URL(string: "https://pub-xxxx.r2.dev")!
            let resolver = PublicBucketResolver(baseURL: base)
            let dbURL = URL.cachesDirectory.appending(path: "parso/library.db")

            // Download once; on later launches open the cached copy directly.
            let lib: ParsoLibrary
            if FileManager.default.fileExists(atPath: dbURL.path) {
                lib = try ParsoLibrary(path: dbURL, audioResolver: resolver)
            } else {
                lib = try await ParsoLibrary.download(
                    from: base.appending(path: "db/library.db"),
                    to: dbURL, audioResolver: resolver)
            }
            self.library = lib
            self.sources = try lib.sources()
        } catch {
            print("ParsoKit load failed:", error)
        }
    }
}
```

> **Threading:** `ParsoLibrary` reads SQLite synchronously and is not thread-safe
> for concurrent calls; confine a single instance to one actor/queue (e.g.
> `@MainActor` as above), or create one instance per thread. Queries are fast
> (indexed), but for very large result sets you can hop off the main actor with a
> `Task.detached` that owns its own `ParsoLibrary`.

### 4. Browse & search in SwiftUI

```swift
struct SourcesView: View {
    @EnvironmentObject var store: LibraryStore
    var body: some View {
        List(store.sources, id: \.key) { src in
            NavigationLink(src.name) { ComposersView(source: src.key) }
                .badge(src.count)
        }
        .task { await store.load() }
    }
}

struct WorkView: View {
    @EnvironmentObject var store: LibraryStore
    let work: ParsoWork
    @State private var movements: [ParsoTrack] = []
    var body: some View {
        List(movements) { track in
            Button(track.movementTitle ?? track.displayTitle) {
                PlaybackController.shared.play(track, using: store.library!)
            }
        }
        .navigationTitle(work.title)
        .task { movements = (try? store.library?.tracks(workID: work.id)) ?? [] }
    }
}
```

Search returns fully-qualified `displayTitle`s, so a search results list needs no
extra formatting:

```swift
let hits = try store.library!.search(queryText)   // [ParsoTrack]
// hits[i].displayTitle == "Beethoven — Symphony No. 5 in C minor, Op. 67 · I. Allegro con brio"
```

### 5. Play with AVFoundation

ParsoKit hands you a URL; you own the player. The bytes are Opus-in-CAF
(`audio/x-caf`), which `AVPlayer`/`AVAudioFile` decode natively on Apple
platforms — no extra codec needed.

```swift
import AVFoundation

final class PlaybackController {
    static let shared = PlaybackController()
    private var player: AVPlayer?

    func play(_ track: ParsoTrack, using library: ParsoLibrary) {
        guard let asset = library.audioAsset(for: track) else { return }
        let item = AVPlayerItem(url: asset.url)        // streams from R2
        let player = AVPlayer(playerItem: item)
        self.player = player
        player.play()
    }
}
```

### 6. Background & lock-screen playback (iOS)

To keep audio playing when backgrounded and show Now Playing controls:

1. Enable the **Audio, AirPlay, and Picture in Picture** background mode
   (target ▸ Signing & Capabilities ▸ Background Modes).
2. Activate an audio session:

```swift
try AVAudioSession.sharedInstance().setCategory(.playback)
try AVAudioSession.sharedInstance().setActive(true)
```

3. Populate `MPNowPlayingInfoCenter.default().nowPlayingInfo` from the track
   metadata (`track.displayTitle`, `track.composer`, `track.durationSec`) and wire
   `MPRemoteCommandCenter` play/pause/seek commands to your `AVPlayer`.

### 7. Offline caching (optional)

`AVPlayer` streams by range request and does not persist the file. For offline
playback, download the CAF yourself and play the local file:

```swift
func localOrRemoteURL(for track: ParsoTrack, library: ParsoLibrary) async throws -> URL {
    let cached = URL.cachesDirectory.appending(path: "parso-audio/\(track.id).caf")
    if FileManager.default.fileExists(atPath: cached.path) { return cached }
    guard let asset = library.audioAsset(for: track) else { throw ParsoError.download("no resolver") }
    let (tmp, _) = try await URLSession.shared.download(from: asset.url)
    try FileManager.default.createDirectory(at: cached.deletingLastPathComponent(),
                                            withIntermediateDirectories: true)
    try FileManager.default.moveItem(at: tmp, to: cached)
    return cached
}
```

For durable, resumable, or background downloads use `AVAssetDownloadURLSession`
or a background `URLSession`.

### 8. Presigned URLs (private bucket)

For a private bucket, don't ship credentials in the app. Either:

- **Public/CDN read** (simplest): make the bucket (or an `audio/` path) publicly
  readable and use `PublicBucketResolver`; or
- **Presign on a backend:** have your server return a short-lived presigned URL,
  and implement `ParsoAudioResolver` to fetch/return it:

```swift
struct BackendResolver: ParsoAudioResolver {
    func audioURL(for track: ParsoTrack) -> URL {
        // Return a presigned URL for "audio/\(track.id).caf" obtained from your API.
        // (Kept synchronous here; prefetch/caches these if your API is async.)
        myPresignedURLCache[track.id]!
    }
}
```

Keys are always `audio/<track.id>.caf`; the DB object is `db/library.db`.

## Public API

| Type | Purpose |
|------|---------|
| `ParsoLibrary` | Open a DB; `sources()`, `composers(source:)`, `works(source:composer:)`, `tracks(workID:)`, `search(_:limit:)`, `track(id:)`, `audioAsset(for:)`. |
| `ParsoTrack` | A playable recording (movement) with `displayTitle`, `composer`, `workTitle`, `catalog`, `movementIndex`, `durationSec`, `cafPath`. |
| `ParsoWork` | A grouped work (its tracks are movements). |
| `ParsoBrowseEntry` | A browse node (`name`, `key`, `count`). |
| `ParsoAudioAsset` | `{ url, contentType, trackID }` — feed `url` to AVFoundation. |
| `ParsoAudioResolver` | Protocol to map a track → audio URL. |
| `PublicBucketResolver` | Built-in resolver for a public/CDN base URL. Supply your own for presigned URLs. |

### Presigned URLs

For a private bucket, implement `ParsoAudioResolver` and return a presigned GET
URL (sign with your credentials via CryptoKit, or fetch one from your backend).
Keys are stored under `audio/<track.id>.caf`.

## Bucket layout

```
audio/<ulid>.caf     # one CAF (Opus-in-CAF) per canonical track
db/library.db        # the distribution SQLite database
```
