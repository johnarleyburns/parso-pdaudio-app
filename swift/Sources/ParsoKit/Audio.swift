import Foundation

/// A streamable audio asset descriptor. ParsoKit does NOT play audio; hand the
/// `url` to AVFoundation (`AVURLAsset` / `AVPlayerItem` / `AVAudioFile`). The
/// bytes are Opus-in-CAF, which AVFoundation decodes natively on Apple platforms.
public struct ParsoAudioAsset: Sendable {
    public let url: URL
    public let contentType: String // "audio/x-caf"
    public let trackID: String
}

/// Resolves a track to a fetchable audio URL. Provide your own to support
/// presigned URLs; ParsoKit ships a public-bucket resolver.
public protocol ParsoAudioResolver: Sendable {
    func audioURL(for track: ParsoTrack) -> URL
}

/// Builds unauthenticated URLs against a public R2 bucket (or CDN) base.
/// Example base: `https://pub-xxxx.r2.dev` or a custom domain. Keys are stored
/// under the `audio/` prefix as `<id>.caf`.
public struct PublicBucketResolver: ParsoAudioResolver {
    public let baseURL: URL
    public let prefix: String

    public init(baseURL: URL, prefix: String = "audio") {
        self.baseURL = baseURL
        self.prefix = prefix
    }

    public func audioURL(for track: ParsoTrack) -> URL {
        baseURL
            .appendingPathComponent(prefix)
            .appendingPathComponent("\(track.id).caf")
    }
}

public enum ParsoError: Error, CustomStringConvertible {
    case database(String)
    case download(String)

    public var description: String {
        switch self {
        case .database(let m): return "ParsoKit database error: \(m)"
        case .download(let m): return "ParsoKit download error: \(m)"
        }
    }
}
