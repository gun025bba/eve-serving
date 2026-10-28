import Foundation
#if canImport(Darwin)
import Darwin
#else
import Glibc
#endif

public enum GuardError: Error, Equatable, CustomStringConvertible {
    case socketPathExists
    case lstatFailed(Int32)
    case dirNotDirectory
    case dirMode(UInt16)
    case dirOwner
    case dirGroup
    case socketGroup
    case peerDenied

    public var description: String {
        switch self {
        case .socketPathExists: return "socket path already exists (refusing to start; remove it manually)"
        case let .lstatFailed(e): return "lstat failed errno=\(e)"
        case .dirNotDirectory: return "socket dir is not a directory (symlinks refused)"
        case let .dirMode(m): return "socket dir mode \(String(m, radix: 8)) != 750"
        case .dirOwner: return "socket dir owner mismatch"
        case .dirGroup: return "socket dir group mismatch"
        case .socketGroup: return "socket group mismatch"
        case .peerDenied: return "peer uid not allowed"
        }
    }
}

public enum PathGuard {
    /// refuseExisting fails if anything (file, socket, symlink) exists at
    /// path. Nothing is ever deleted automatically.
    public static func refuseExisting(_ path: String) throws {
        var st = stat()
        if lstat(path, &st) == 0 { throw GuardError.socketPathExists }
        if errno != ENOENT { throw GuardError.lstatFailed(errno) }
    }

    /// checkDir requires a real directory (not a symlink), mode exactly
    /// 0750, owned by owner:group.
    public static func checkDir(_ path: String, owner: uid_t, group: gid_t) throws {
        var st = stat()
        guard lstat(path, &st) == 0 else { throw GuardError.lstatFailed(errno) }
        guard (st.st_mode & S_IFMT) == S_IFDIR else { throw GuardError.dirNotDirectory }
        let perm = UInt16(st.st_mode & 0o7777)
        guard perm == 0o750 else { throw GuardError.dirMode(perm) }
        guard st.st_uid == owner else { throw GuardError.dirOwner }
        guard st.st_gid == group else { throw GuardError.dirGroup }
    }

    /// checkSocket verifies the bound socket is a socket in group.
    public static func checkSocket(_ path: String, group: gid_t) throws {
        var st = stat()
        guard lstat(path, &st) == 0 else { throw GuardError.lstatFailed(errno) }
        guard (st.st_mode & S_IFMT) == S_IFSOCK, st.st_gid == group else { throw GuardError.socketGroup }
    }
}

public enum PeerPolicy {
    /// allowed requires an exact uid match against the allowlist. An empty
    /// allowlist allows nobody.
    public static func allowed(peerUID: uid_t, allowlist: Set<uid_t>) -> Bool {
        allowlist.contains(peerUID)
    }
}
