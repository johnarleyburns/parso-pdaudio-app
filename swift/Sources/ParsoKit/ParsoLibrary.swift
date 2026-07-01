import Foundation

/// ParsoLibrary is the entry point: open a parso distribution database, browse
/// and search its tracks, and resolve streamable audio URLs for AVFoundation.
///
/// The database can come from three places:
///   * a file you already have on disk (`init(path:)`),
///   * a bundled resource (`init(path:)` with `Bundle.url(forResource:)`),
///   * a download from R2 (`download(from:to:)`).
///
/// ParsoLibrary never plays audio; use `audioAsset(for:)` to get a URL to hand
/// to AVFoundation.
public final class ParsoLibrary {
    private let db: SQLiteDB
    private let resolver: ParsoAudioResolver?

    /// Open a library from a local SQLite file. `resolver` is required to build
    /// audio URLs (`audioAsset(for:)`); browsing/searching work without it.
    public init(path: URL, audioResolver: ParsoAudioResolver? = nil) throws {
        self.db = try SQLiteDB(path: path.path)
        self.resolver = audioResolver
    }

    /// Download the distribution DB from a URL (e.g. a public R2 object at
    /// `db/library.db`) to `destination`, then open it.
    public static func download(from url: URL, to destination: URL,
                                audioResolver: ParsoAudioResolver? = nil,
                                session: URLSession = .shared) async throws -> ParsoLibrary {
        let (temp, response) = try await session.download(from: url)
        if let http = response as? HTTPURLResponse, !(200...299).contains(http.statusCode) {
            throw ParsoError.download("HTTP \(http.statusCode) fetching \(url)")
        }
        try? FileManager.default.removeItem(at: destination)
        try FileManager.default.createDirectory(
            at: destination.deletingLastPathComponent(), withIntermediateDirectories: true)
        try FileManager.default.moveItem(at: temp, to: destination)
        return try ParsoLibrary(path: destination, audioResolver: audioResolver)
    }

    private static let playable = "status='done' AND caf_path IS NOT NULL AND dup_of IS NULL"

    private static func trackCols(_ p: String = "") -> String {
        """
        \(p)id, \(p)source, \(p)title, \(p)composer, \(p)work_id, \(p)work_title, \(p)catalog,
        COALESCE(\(p)movement_index,0), \(p)movement_title, \(p)display_title,
        COALESCE(\(p)duration_sec,0), \(p)caf_path
        """
    }

    private func mapTrack(_ r: Row) -> ParsoTrack {
        let composer = r.optString(3) ?? ""
        let title = r.string(2)
        let display = r.optString(9) ?? (composer.isEmpty ? title : "\(composer) — \(title)")
        return ParsoTrack(
            id: r.string(0), source: r.string(1), title: title, composer: composer,
            workID: r.optString(4), workTitle: r.optString(5), catalog: r.optString(6),
            movementIndex: r.int(7), movementTitle: r.optString(8), displayTitle: display,
            durationSec: r.double(10), cafPath: r.string(11))
    }

    // MARK: - Browse

    /// Top-level sources with playable counts.
    public func sources() throws -> [ParsoBrowseEntry] {
        try db.query("""
        SELECT source, COUNT(*) FROM tracks WHERE \(Self.playable)
        GROUP BY source ORDER BY source COLLATE NOCASE
        """) { ParsoBrowseEntry(name: $0.string(0), key: $0.string(0), count: $0.int(1)) }
    }

    /// Composers within a source.
    public func composers(source: String) throws -> [ParsoBrowseEntry] {
        try db.query("""
        SELECT composer, COUNT(*) FROM tracks
        WHERE \(Self.playable) AND source=?
        GROUP BY composer ORDER BY composer COLLATE NOCASE
        """, [source]) {
            let c = $0.optString(0) ?? ""
            return ParsoBrowseEntry(name: c.isEmpty ? "—" : c, key: c, count: $0.int(1))
        }
    }

    /// Works (grouped movements) within a source+composer.
    public func works(source: String, composer: String) throws -> [ParsoWork] {
        try db.query("""
        SELECT COALESCE(w.id, 'title:'||t.title) AS id,
               COALESCE(w.composer_canonical, t.composer, '') AS composer,
               COALESCE(w.title, t.work_title, t.title) AS title,
               w.catalog, COUNT(*) AS n
          FROM tracks t LEFT JOIN works w ON w.id = t.work_id
         WHERE t.\(Self.playable) AND t.source=? AND (t.composer IS ? OR t.composer = ?)
         GROUP BY id
         ORDER BY title COLLATE NOCASE
        """, [source, composer, composer]) {
            ParsoWork(id: $0.string(0), composer: $0.string(1), title: $0.string(2),
                      catalog: $0.optString(3), trackCount: $0.int(4))
        }
    }

    /// The tracks (movements) of a work, ordered by movement index. The `workID`
    /// is a `works.id` or a `"title:<title>"` key returned by `works(...)`.
    public func tracks(workID: String) throws -> [ParsoTrack] {
        if workID.hasPrefix("title:") {
            let title = String(workID.dropFirst("title:".count))
            return try db.query("""
            SELECT \(Self.trackCols()) FROM tracks
            WHERE \(Self.playable) AND title = ?
            ORDER BY COALESCE(movement_index,0), title COLLATE NOCASE
            """, [title], mapTrack)
        }
        return try db.query("""
        SELECT \(Self.trackCols()) FROM tracks
        WHERE \(Self.playable) AND work_id = ?
        ORDER BY COALESCE(movement_index,0), title COLLATE NOCASE
        """, [workID], mapTrack)
    }

    // MARK: - Search / lookup

    /// Full-text search over the tracks_fts index, returning playable tracks.
    public func search(_ query: String, limit: Int = 200) throws -> [ParsoTrack] {
        let match = Self.buildMatch(query)
        if match.isEmpty {
            return try db.query("""
            SELECT \(Self.trackCols()) FROM tracks WHERE \(Self.playable)
            ORDER BY source, composer, work_title, title LIMIT \(limit)
            """, [], mapTrack)
        }
        return try db.query("""
        SELECT \(Self.trackCols("t.")) FROM tracks_fts f
        JOIN tracks t ON t.id = f.track_id
        WHERE f.body MATCH ? AND t.status='done' AND t.caf_path IS NOT NULL AND t.dup_of IS NULL
        ORDER BY rank LIMIT \(limit)
        """, [match], mapTrack)
    }

    /// Look up a single track by id.
    public func track(id: String) throws -> ParsoTrack? {
        try db.query("SELECT \(Self.trackCols()) FROM tracks WHERE id = ?", [id], mapTrack).first
    }

    // MARK: - Audio

    /// Build a streamable asset descriptor for a track. Requires an audio
    /// resolver (passed at init). The URL points at the track's CAF in R2.
    public func audioAsset(for track: ParsoTrack) -> ParsoAudioAsset? {
        guard let resolver else { return nil }
        return ParsoAudioAsset(url: resolver.audioURL(for: track),
                               contentType: "audio/x-caf", trackID: track.id)
    }

    // MARK: - Helpers

    /// buildMatch turns free user text into a safe FTS5 prefix query (mirrors the
    /// Go implementation).
    static func buildMatch(_ q: String) -> String {
        let terms = q.lowercased().split(whereSeparator: { !$0.isLetter && !$0.isNumber })
            .map { String($0.unicodeScalars.filter { CharacterSet.alphanumerics.contains($0) }) }
            .filter { !$0.isEmpty }
            .map { $0 + "*" }
        return terms.joined(separator: " ")
    }
}
