import Foundation
import SQLite3

/// Minimal read-only SQLite wrapper used by ParsoLibrary. Not part of the public
/// API surface beyond what ParsoLibrary exposes.
final class SQLiteDB {
    private var handle: OpaquePointer?

    static let transient = unsafeBitCast(-1, to: sqlite3_destructor_type.self)

    init(path: String) throws {
        let flags = SQLITE_OPEN_READONLY | SQLITE_OPEN_NOMUTEX
        guard sqlite3_open_v2(path, &handle, flags, nil) == SQLITE_OK else {
            let msg = handle.map { String(cString: sqlite3_errmsg($0)) } ?? "unknown"
            sqlite3_close(handle)
            throw ParsoError.database("open failed: \(msg)")
        }
    }

    deinit {
        sqlite3_close(handle)
    }

    /// query runs SQL with positional string parameters and maps each row.
    func query<T>(_ sql: String, _ params: [String] = [], _ map: (Row) -> T) throws -> [T] {
        var stmt: OpaquePointer?
        guard sqlite3_prepare_v2(handle, sql, -1, &stmt, nil) == SQLITE_OK else {
            throw ParsoError.database("prepare failed: \(String(cString: sqlite3_errmsg(handle)))")
        }
        defer { sqlite3_finalize(stmt) }
        for (i, p) in params.enumerated() {
            sqlite3_bind_text(stmt, Int32(i + 1), p, -1, SQLiteDB.transient)
        }
        var out: [T] = []
        while sqlite3_step(stmt) == SQLITE_ROW {
            out.append(map(Row(stmt: stmt)))
        }
        return out
    }
}

/// Row is a thin accessor over a stepped sqlite3 statement.
struct Row {
    let stmt: OpaquePointer?

    func string(_ index: Int32) -> String {
        guard let c = sqlite3_column_text(stmt, index) else { return "" }
        return String(cString: c)
    }

    func optString(_ index: Int32) -> String? {
        guard sqlite3_column_type(stmt, index) != SQLITE_NULL,
              let c = sqlite3_column_text(stmt, index) else { return nil }
        return String(cString: c)
    }

    func int(_ index: Int32) -> Int {
        Int(sqlite3_column_int64(stmt, index))
    }

    func double(_ index: Int32) -> Double {
        sqlite3_column_double(stmt, index)
    }
}
