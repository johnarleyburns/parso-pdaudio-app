import XCTest
@testable import ParsoKit

final class ParsoKitTests: XCTestCase {
    func testBuildMatch() {
        XCTAssertEqual(ParsoLibrary.buildMatch("Beethoven Symphony 5"), "beethoven* symphony* 5*")
        XCTAssertEqual(ParsoLibrary.buildMatch("  op. 67 !!"), "op* 67*")
        XCTAssertEqual(ParsoLibrary.buildMatch(""), "")
    }

    func testPublicBucketResolver() {
        let base = URL(string: "https://pub-example.r2.dev")!
        let resolver = PublicBucketResolver(baseURL: base)
        let track = ParsoTrack(
            id: "01ABC", source: "chopin", title: "Ballade", composer: "Chopin",
            workID: nil, workTitle: nil, catalog: nil, movementIndex: 0,
            movementTitle: nil, displayTitle: "Chopin — Ballade", durationSec: 100,
            cafPath: "01ABC.caf")
        XCTAssertEqual(resolver.audioURL(for: track).absoluteString,
                       "https://pub-example.r2.dev/audio/01ABC.caf")
    }

    // Opens a DB if PARSO_TEST_DB points at a real distribution DB; skipped otherwise.
    func testOpenAndBrowseIfDBPresent() throws {
        guard let path = ProcessInfo.processInfo.environment["PARSO_TEST_DB"] else {
            throw XCTSkip("set PARSO_TEST_DB to run the integration query test")
        }
        let lib = try ParsoLibrary(path: URL(fileURLWithPath: path))
        let sources = try lib.sources()
        XCTAssertFalse(sources.isEmpty, "expected at least one source")
        if let first = sources.first {
            _ = try lib.composers(source: first.key)
        }
        _ = try lib.search("bach")
    }
}
