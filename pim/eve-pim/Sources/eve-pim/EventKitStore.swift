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
            Self.event($0)
        }
    }

    static func event(_ e: EKEvent) -> PimEvent {
        let end = e.isAllDay ? allDayExclusiveEnd(start: e.startDate, end: e.endDate, tz: .current) : e.endDate!
        return PimEvent(id: e.calendarItemIdentifier, title: e.title ?? "", start: e.startDate, end: end,
                        allDay: e.isAllDay, calendar: e.calendar?.title ?? "", calendarID: e.calendar?.calendarIdentifier ?? "")
    }

    func calendars() throws -> [PimCalendar] {
        guard Self.authorized(.event) else { throw PimError(.permissionDenied) }
        return ek.calendars(for: .event).map {
            PimCalendar(id: $0.calendarIdentifier, name: $0.title, writable: $0.allowsContentModifications)
        }
    }

    func addEvent(title: String, start: Date, end: Date, allDay: Bool, location: String?, notes: String?,
                  calendarID: String?) throws -> PimEvent {
        guard Self.authorized(.event) else { throw PimError(.permissionDenied) }
        let cal: EKCalendar
        if let calendarID {
            guard let c = ek.calendar(withIdentifier: calendarID), c.allowsContentModifications,
                  ek.calendars(for: .event).contains(where: { $0.calendarIdentifier == c.calendarIdentifier })
            else { throw PimError(.notFound) }
            cal = c
        } else {
            guard let c = ek.defaultCalendarForNewEvents else { throw PimError(.notFound) }
            cal = c
        }
        let ev = EKEvent(eventStore: ek)
        ev.calendar = cal
        ev.title = title
        ev.isAllDay = allDay
        ev.startDate = start
        ev.endDate = allDay ? end.addingTimeInterval(-1) : end // all-day: the last second of the last day
        ev.location = location
        ev.notes = notes
        do { try ek.save(ev, span: .thisEvent, commit: true) } catch { throw PimError(.internalError) }
        return Self.event(ev)
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
