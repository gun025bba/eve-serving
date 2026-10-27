import Foundation

// Wire protocol (one request per connection):
//   request:  {"v":1,"id":"<=64 chars","method":"<method>","params":{...}}\n
//   response: {"v":1,"id":"...","ok":true,"result":{...}}\n
//          or {"v":1,"id":"...","ok":false,"error":"<code>"}\n
// The schema is fixed: unknown top-level keys, unknown params, unknown
// methods and wrong types are rejected. Requests are capped at 8 KiB.

public enum PimMethod: String, CaseIterable, Sendable {
    case listEvents = "list_events"
    case listReminders = "list_reminders"
    case addReminder = "add_reminder"
    case completeReminder = "complete_reminder"

    public var isWrite: Bool { self == .addReminder || self == .completeReminder }

    /// Exact params keys per method (required + optional).
    var allowedParams: Set<String> {
        switch self {
        case .listEvents: return ["from", "to"]
        case .listReminders: return ["due_before", "include_done"]
        case .addReminder: return ["title", "due", "list"]
        case .completeReminder: return ["id"]
        }
    }
}

public enum PimErrorCode: String, Sendable {
    case invalidRequest = "invalid_request"
    case unknownMethod = "unknown_method"
    case tooLarge = "too_large"
    case rangeTooLong = "range_too_long"
    case permissionDenied = "permission_denied"
    case writesDisabled = "writes_disabled"
    case notFound = "not_found"
    case internalError = "internal"
}

public struct PimError: Error, Equatable, Sendable {
    public let code: PimErrorCode
    public init(_ code: PimErrorCode) { self.code = code }
}

public enum Limits {
    public static let maxRequestBytes = 8 * 1024
    public static let maxEvents = 50
    public static let maxReminders = 100
    public static let maxRangeSeconds: TimeInterval = 31 * 24 * 3600
    public static let maxTitleChars = 200
    public static let maxIDChars = 256
    public static let maxListChars = 100
    public static let maxRequestIDChars = 64
}

public enum PimRequest: Equatable, Sendable {
    case listEvents(from: Date, to: Date)
    case listReminders(dueBefore: Date?, includeDone: Bool)
    case addReminder(title: String, due: Date?, list: String?)
    case completeReminder(id: String)

    public var method: PimMethod {
        switch self {
        case .listEvents: return .listEvents
        case .listReminders: return .listReminders
        case .addReminder: return .addReminder
        case .completeReminder: return .completeReminder
        }
    }
}

public struct ParsedRequest: Equatable, Sendable {
    public let id: String
    public let request: PimRequest
}

enum RFC3339 {
    static func parse(_ s: String) -> Date? {
        let f = ISO8601DateFormatter()
        f.formatOptions = [.withInternetDateTime]
        return f.date(from: s)
    }

    static func format(_ d: Date, tz: TimeZone) -> String {
        let f = ISO8601DateFormatter()
        f.formatOptions = [.withInternetDateTime]
        f.timeZone = tz
        return f.string(from: d)
    }
}

private struct Envelope: Decodable {
    let v: Int
    let id: String
    let method: String
}

private struct ListEventsParams: Decodable { let from: String; let to: String }
private struct ListRemindersParams: Decodable { let due_before: String?; let include_done: Bool? }
private struct AddReminderParams: Decodable { let title: String; let due: String?; let list: String? }
private struct CompleteReminderParams: Decodable { let id: String }
private struct ParamsWrapper<P: Decodable>: Decodable { let params: P }

/// The id of a request, if it can be read, so that error responses can echo it.
public func requestID(_ data: Data) -> String {
    guard data.count <= Limits.maxRequestBytes,
          let obj = try? JSONSerialization.jsonObject(with: data) as? [String: Any],
          let id = obj["id"] as? String, id.count <= Limits.maxRequestIDChars
    else { return "" }
    return id
}

/// parseRequest validates one request strictly. It never logs or echoes
/// field values other than the request id.
public func parseRequest(_ raw: Data) throws -> ParsedRequest {
    if raw.count > Limits.maxRequestBytes { throw PimError(.tooLarge) }
    var data = raw
    if data.last == UInt8(ascii: "\n") { data.removeLast() }
    guard let obj = (try? JSONSerialization.jsonObject(with: data)) as? [String: Any] else {
        throw PimError(.invalidRequest)
    }
    guard Set(obj.keys) == ["v", "id", "method", "params"] else { throw PimError(.invalidRequest) }
    let dec = JSONDecoder()
    guard let env = try? dec.decode(Envelope.self, from: data), env.v == 1,
          !env.id.isEmpty, env.id.count <= Limits.maxRequestIDChars
    else { throw PimError(.invalidRequest) }
    guard let method = PimMethod(rawValue: env.method) else { throw PimError(.unknownMethod) }
    guard let params = obj["params"] as? [String: Any],
          Set(params.keys).isSubset(of: method.allowedParams)
    else { throw PimError(.invalidRequest) }

    func decode<P: Decodable>(_: P.Type) throws -> P {
        guard let w = try? dec.decode(ParamsWrapper<P>.self, from: data) else { throw PimError(.invalidRequest) }
        return w.params
    }
    func date(_ s: String?) throws -> Date? {
        guard let s else { return nil }
        guard let d = RFC3339.parse(s) else { throw PimError(.invalidRequest) }
        return d
    }
    func text(_ s: String?, max: Int) throws -> String? {
        guard let s else { return nil }
        let t = s.trimmingCharacters(in: .whitespacesAndNewlines)
        if t.isEmpty || t.count > max || t.unicodeScalars.contains(where: { CharacterSet.controlCharacters.contains($0) }) {
            throw PimError(.invalidRequest)
        }
        return t
    }

    let req: PimRequest
    switch method {
    case .listEvents:
        let p = try decode(ListEventsParams.self)
        guard let from = try date(p.from), let to = try date(p.to), to > from else { throw PimError(.invalidRequest) }
        if to.timeIntervalSince(from) > Limits.maxRangeSeconds { throw PimError(.rangeTooLong) }
        req = .listEvents(from: from, to: to)
    case .listReminders:
        let p = try decode(ListRemindersParams.self)
        req = .listReminders(dueBefore: try date(p.due_before), includeDone: p.include_done ?? false)
    case .addReminder:
        let p = try decode(AddReminderParams.self)
        guard let title = try text(p.title, max: Limits.maxTitleChars) else { throw PimError(.invalidRequest) }
        req = .addReminder(title: title, due: try date(p.due), list: try text(p.list, max: Limits.maxListChars))
    case .completeReminder:
        let p = try decode(CompleteReminderParams.self)
        guard let id = try text(p.id, max: Limits.maxIDChars) else { throw PimError(.invalidRequest) }
        req = .completeReminder(id: id)
    }
    return ParsedRequest(id: env.id, request: req)
}
