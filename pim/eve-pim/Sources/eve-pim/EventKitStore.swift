import EventKit
import EvePimCore
import Foundation

/// EventKitStore is the only EventKit user. It returns the minimal fields
/// (no notes, attendees, location or URL).
final class EventKitStore: PimStore {
    let ek = EKEventStore()

    static func authorized(_ type: EKEntityType) -> Bool {
        let st = EKEventStore.authorizationStatus(for: type)
        if #available(macOS 14.0, *) {
            return st == .fullAccess
        }
        return st == .authorized
    }

    /// requestAccess asks once per entity type (TCC prompt in the user's
    /// login session). It blocks until the user answers or the timeout.
    func requestAccess() {
        for type in [EKEntityType.event, .reminder] where !Self.authorized(type) {
            let sem = DispatchSemaphore(value: 0)
            let done: (Bool, Error?) -> Void = { _, _ in sem.signal() }
            if #available(macOS 14.0, *) {
                if type == .event {
                    ek.requestFullAccessToEvents(completion: done)
                } else {
                    ek.requestFullAccessToReminders(completion: done)
                }
            } else {
                ek.requestAccess(to: type, completion: done)
            }
            _ = sem.wait(timeout: .now() + 300)
        }
    }

    func events(from: Date, to: Date, limit: Int) throws -> [PimEvent] {
        guard Self.authorized(.event) else { throw PimError(.permissionDenied) }
        let pred = ek.predicateForEvents(withStart: from, end: to, calendars: nil)
        let evs = ek.events(matching: pred).sorted { $0.startDate < $1.startDate }
        return evs.prefix(limit).map {
            PimEvent(id: $0.calendarItemIdentifier, title: $0.title ?? "", start: $0.startDate,
                     end: $0.endDate, allDay: $0.isAllDay, calendar: $0.calendar?.title ?? "")
        }
    }

    func reminders(dueBefore: Date?, includeDone: Bool, limit: Int) throws -> [PimReminder] {
        guard Self.authorized(.reminder) else { throw PimError(.permissionDenied) }
        let pred: NSPredicate
        if includeDone {
            pred = ek.predicateForReminders(in: nil)
        } else {
            pred = ek.predicateForIncompleteReminders(withDueDateStarting: nil, ending: dueBefore, calendars: nil)
        }
        let sem = DispatchSemaphore(value: 0)
        var got: [EKReminder] = []
        ek.fetchReminders(matching: pred) { rs in
            got = rs ?? []
            sem.signal()
        }
        if sem.wait(timeout: .now() + 10) == .timedOut { throw PimError(.internalError) }
        let out = got.map(Self.reminder)
        let sorted = out.sorted { ($0.due ?? .distantFuture) < ($1.due ?? .distantFuture) }
        return Array(sorted.prefix(limit))
    }

    func addReminder(title: String, due: Date?, list: String?) throws -> PimReminder {
        guard Self.authorized(.reminder) else { throw PimError(.permissionDenied) }
        let r = EKReminder(eventStore: ek)
        r.title = title
        if let list {
            guard let cal = ek.calendars(for: .reminder).first(where: { $0.title == list }) else {
                throw PimError(.notFound)
            }
            r.calendar = cal
        } else {
            guard let cal = ek.defaultCalendarForNewReminders() else { throw PimError(.notFound) }
            r.calendar = cal
        }
        if let due {
            r.dueDateComponents = Calendar.current.dateComponents(
                [.year, .month, .day, .hour, .minute, .timeZone], from: due)
        }
        do { try ek.save(r, commit: true) } catch { throw PimError(.internalError) }
        return Self.reminder(r)
    }

    func completeReminder(id: String) throws -> PimReminder {
        guard Self.authorized(.reminder) else { throw PimError(.permissionDenied) }
        guard let r = ek.calendarItem(withIdentifier: id) as? EKReminder else { throw PimError(.notFound) }
        r.isCompleted = true
        do { try ek.save(r, commit: true) } catch { throw PimError(.internalError) }
        return Self.reminder(r)
    }

    static func reminder(_ r: EKReminder) -> PimReminder {
        let due = r.dueDateComponents.flatMap { Calendar.current.date(from: $0) }
        return PimReminder(id: r.calendarItemIdentifier, title: r.title ?? "", due: due,
                           list: r.calendar?.title ?? "", done: r.isCompleted)
    }
}
