import Foundation

/// A single playable recording (a movement of a work, or a standalone piece).
public struct ParsoTrack: Identifiable, Hashable, Sendable {
    public let id: String
    public let source: String
    public let title: String
    public let composer: String
    public let workID: String?
    public let workTitle: String?
    public let catalog: String?
    public let movementIndex: Int
    public let movementTitle: String?
    /// Precomputed, fully-qualified display label (composer — work · movement).
    public let displayTitle: String
    public let durationSec: Double
    /// The relative CAF filename in the bucket's `audio/` prefix (`<id>.caf`).
    public let cafPath: String

    public init(id: String, source: String, title: String, composer: String,
                workID: String?, workTitle: String?, catalog: String?,
                movementIndex: Int, movementTitle: String?, displayTitle: String,
                durationSec: Double, cafPath: String) {
        self.id = id
        self.source = source
        self.title = title
        self.composer = composer
        self.workID = workID
        self.workTitle = workTitle
        self.catalog = catalog
        self.movementIndex = movementIndex
        self.movementTitle = movementTitle
        self.displayTitle = displayTitle
        self.durationSec = durationSec
        self.cafPath = cafPath
    }
}

/// A grouped musical work (e.g. a symphony) whose tracks are its movements.
public struct ParsoWork: Identifiable, Hashable, Sendable {
    public let id: String
    public let composer: String
    public let title: String
    public let catalog: String?
    public let trackCount: Int
}

/// A named node in the browse tree (a source, composer, or work) with a count.
public struct ParsoBrowseEntry: Hashable, Sendable {
    /// Display name.
    public let name: String
    /// Opaque key to pass back into the next browse call.
    public let key: String
    /// Number of playable tracks under this node.
    public let count: Int
}
