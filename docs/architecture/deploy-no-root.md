# 배포에서 root 제거 설계 (root는 최초 설치에서만)

> 상태: **설계 제안 (rev 1, 미구현)** · 작성: 시스템 아키텍트 · 티켓: Notion `[OPS] 건영 sudo 제로화: 배포 도구 자가 갱신 설계` (담당 DevOps, 같은 티켓에 DevOps 초안과 아키텍트 의견 절이 따로 있음) · 관련 PR: #168(one-shot 단계 추가, DevOps)
>
> **SemVer 판정: MAJOR 예상. 보안 경계(root 소유 검증기 → 서비스 계정 권한)가 바뀌므로 보안 담당 리뷰가 필수입니다.** 불확실한 항목은 모두 MAJOR로 둡니다. MAJOR 배포는 건영님이 직접 확인한 뒤에만 합니다. 이 문서는 설계만 담고, 코드·배포·host 변경은 포함하지 않습니다.

## 0. 요약

- 지금은 배포 때마다 `sudo`가 한 점 있습니다. 서명 검증·파일 교체·서비스 재시작이 모두 **root로 도는 진입점 하나**(`/usr/local/libexec/eve/deploy`)에 모여 있고, sudoers의 NOPASSWD 4개 동사(`deploy`, `deploy-web`, `rollback`, `rollback-web`)가 그 문을 엽니다.
- 목표: **root는 최초 설치(1회)에서만** 쓰고, 이후 배포·롤백은 서비스 계정(`evedeploy`)만으로 합니다. sudoers 규칙은 없앱니다.
- 핵심 설계 결정 4가지
  1. **root 소유는 "신뢰의 뿌리"로 한정하고 한 번만 설치**: cosign, trust root, 런처(검증 후 실행), launchd plist. 이것이 바뀌는 일(= 코어 변경)만 사람 root입니다.
  2. **바이너리·정적 파일은 `evedeploy`가 소유**하는 `releases/` 아래 버전 디렉터리에 두고, `live/` 심볼릭 링크를 원자적으로 바꿔 전환합니다.
  3. **서비스는 LaunchDaemon + `UserName=_evegw`**로 두고, 재시작은 sudo 없이 **런처가 링크 변경을 감지해 자식 프로세스를 교체**합니다. 이때 런처가 **실행 시점에 서명·해시를 다시 검증**하므로, `evedeploy`가 뚫려도 서명되지 않은 코드는 서비스 계정으로 실행되지 않습니다.
  4. **`evedeploy`는 키·DB·설정을 쓸 수 없습니다**. 계정마다 소유 경계를 가르고(§4, §9), 브레이크(PAUSE·floor·blocklist)는 "조이기"와 "풀기"의 권한을 나눕니다(§5.3).
- DevOps 초안(티켓 §3, "코어 고정 + 페이로드 자동 갱신")과 방향은 같습니다. 다른 점은 **페이로드를 root로 실행하지 않는다**는 것입니다. 이 문서의 모델에서는 페이로드(poller, fetch, 헬퍼)가 `evedeploy` 권한으로만 돌기 때문에, 초안의 남는 위험 "서명된 악성 페이로드가 root로 실행"이 구조적으로 사라집니다.

## 1. 현재 root 의존 지점 (전수 목록)

레포(`eve-serving` main)와 `eve-web`을 읽어 정리했습니다. 근거 경로는 `deploy/nopasswd/` 기준입니다.

| # | 지점 | 근거 | root가 필요한 이유 |
|---|---|---|---|
| R1 | sudoers `NOPASSWD` 4개 동사 | `setup-deploy-nopasswd.sh` §5, `/etc/sudoers.d/eve-deploy` | 파일 위치 자체가 root 소유 |
| R2 | 엔트리포인트 실행 | `eve-deploy`의 `require_root` | 검증기가 root로 돌아야 root 소유 디렉터리에 쓸 수 있음 |
| R3 | 바이너리 교체 `/opt/eve-gw/bin` (root:wheel 0755) | `eve-deploy` `BIN_DIR`, `swap_and_check` | 디렉터리 소유가 root. `_evegw`가 자기 코드를 못 바꾸게 하려던 의도 |
| R4 | 웹 트리 교체 `/opt/eve-gw/web` (`chown -R root:wheel`) | `eve-deploy` `do_deploy_web`, `web_swap_and_check` | 위와 동일 |
| R5 | 서비스 재시작 `launchctl kickstart -k system/com.llmserving.gateway` | `eve-deploy` `svc_restart` | system 도메인 서비스 조작 |
| R6 | 상태·브레이크 `/var/db/eve-deploy` (root 0700): `current_version`, `schema`, `PAUSE`, `blocklist`, `floor*`, `previous*`, `allowed_user` | `eve-deploy` `STATE`, `hold`, `check_floor` | 호출자가 못 고치게 하려는 의도 |
| R7 | 로그 `/var/log/eve-deploy.log` (root 0600) | `eve-deploy` `LOGF` | 위와 동일 |
| R8 | 도구 설치 `/usr/local/libexec/eve/{deploy,fetch,poll,uninstall,cosign,trusted_root.json}` (root:wheel) | `setup-deploy-nopasswd.sh` §4, `setup-unattended.sh state` | 신뢰의 뿌리. 이 중 `fetch`·`poll`은 root가 필요 없는 스크립트(`evedeploy`로 실행)인데 위치만 root 소유라서 갱신에 root가 듭니다 |
| R9 | 계정 생성 `dscl`: `evedeploy`, `evebackup` (uid 460-499), `_evegw` (451) | `setup-unattended.sh accounts`, `docs/runbooks/4.0.0-loopback-session.md` §2 | 계정 관리 |
| R10 | LaunchDaemon plist 설치·로드 `/Library/LaunchDaemons` (poller, gateway) | `setup-unattended.sh state`·`load`, 4.0.0 runbook §4 | system 도메인 |
| R11 | 롤백 호출 `sudo -n .../deploy rollback` (poller의 3회 헬스 실패 시) | `eve-poll` `observe` | R1~R5와 같은 문 |
| R12 | 브레이크 해제·조정 `sudo rm .../PAUSE`, `setup-unattended unpause/raise-floor/rehearse` | `docs/runbooks/unattended-deploy.md` | root 소유 상태 |
| R13 | 시크릿 회전: `sudo -e /opt/eve-gw/etc/config.yaml`, api_key `chown/mv`, 이후 `sudo launchctl kickstart` | 4.0.0 commands 표 9·10행 | 파일은 `_evegw` 소유인데 편집에 root, 반영에 root |
| R14 | 선택: pf 규칙(`/etc/pf.conf`, `pfctl`) | 4.0.0 commands 표 8행 | 시스템 설정 |
| R15 | one-shot 설치 스크립트 `eve-oneshot.sh`, `mac-oneshot.sh` | PR #168 포함 | 위 R1·R8~R10을 한 번에 실행하는 sudo 세션 |
| R16 | 문서·워크플로 안내 문구 `sudo .../deploy deploy-web` | `eve-web/.github/workflows/release.yml` 머리말, `deploy/nopasswd/README.md` | 문구만 (S4에서 정리) |

root가 필요하지 않은데 사람 계정에 묶여 있는 것(별도 정리 대상, §9):

| # | 지점 | 근거 |
|---|---|---|
| H1 | PostgreSQL은 `brew services`(건영 로그인 사용자의 LaunchAgent), 데이터는 brew 접두 아래 | `deploy/pg-setup.sh` |
| H2 | `mlx_lm.server`는 `~/Library/LaunchAgents`의 LaunchAgent, venv·모델이 사용자 홈 | `deploy/launchd/com.llmserving.mlx.plist.template` |
| H3 | 일일 백업이 사용자 LaunchAgent, 백업 파일이 사용자 홈 | `deploy/pg-backup.sh`, `install-pgbackup.sh` |
| H4 | 릴리스 태그 deploy key가 사용자 홈 `~/.eve-release-tagger` | `docs/deploy-signing.md` |
| H5 | `tailscale serve` 설정 | `docs/architecture/network-tailscale.md` (이 레포에 설치 절차 없음, 확인 필요) |

## 2. 최초 설치(root)와 일상 배포(서비스 계정)의 경계

원칙: **"무엇을 신뢰할지 정하는 것"은 root가 1회, "신뢰된 것을 적용하는 것"은 서비스 계정이 매번**.

| 구분 | root (최초 1회, 이후 코어 변경 때만) | `evedeploy` (일상 배포) |
|---|---|---|
| 계정 | 계정 생성(R9), 그룹 규칙 | - |
| 코어 파일 | cosign, `trusted_root.json`, 런처, plist, 핀 sha256 | 읽기만 |
| 디렉터리 | `/opt/eve-gw` 뼈대와 소유 지정 | `releases/`, `live/`, 스테이징 안에서만 |
| 서비스 등록 | LaunchDaemon plist 설치·로드 | 없음 |
| 서명 검증 | (검증기 자체를 설치) | fetch → 검증(서명·신원·버전·해시·built_at) → 풀기 |
| 전환·롤백 | - | `live/` 링크 교체, 헬스 확인, 링크 되돌리기 |
| 브레이크 | 초기값 생성 | **조이기만**(§5.3) |
| 시크릿 | 최초 생성 | 접근 없음 |

**코어 변경은 계속 사람 root입니다**(Sigstore trust root 회전, cosign 교체, 런처 수정, 신원 상수 변경). 그래서 코어를 작게 유지하고 변경 때마다 보안 리뷰·핀·롤백 리허설을 거칩니다. 이것은 티켓 §7 결정 4("코어 변경은 사람이라는 예외를 받아들일지")의 아키텍트 답이며, **받아들이는 것을 권장합니다**.

## 3. 서비스 형태: LaunchAgent vs LaunchDaemon + UserName

사실과 아직 확인하지 못한 것을 구분해서 적습니다. 이 메모를 쓴 환경은 macOS host가 아니라서 **launchd 동작은 host에서 검증해야 하는 항목**으로 둡니다(§12).

| 기준 | A. LaunchAgent (서비스 계정) | B. LaunchDaemon + `UserName` + sudo `kickstart` (현재) | **C. LaunchDaemon + `UserName` + 런처가 전환 (권장)** |
|---|---|---|---|
| 로그인 없이 부팅 시 기동 | **아니오.** Agent는 사용자 세션(`gui/<uid>` 또는 `user/<uid>` 도메인)이 있을 때 올라옴. 로그인하지 않는 숨김 서비스 계정은 세션이 없음 (host 검증 필요) | **예.** 레포가 이미 이 방식을 씀: poller plist 주석 "A LaunchDaemon: no login needed" (`com.llmserving.evepoll.plist.template`), gateway도 4.0.0 runbook에서 `/Library/LaunchDaemons` + `UserName=_evegw` | **예** (B와 같음) |
| sudo 없이 재시작 | 같은 계정이 자기 도메인의 서비스를 `kickstart`할 수는 있으나, 세션이 없으면 도메인 자체가 없음 | **아니오.** system 도메인 `kickstart`는 root (`svc_restart`가 바로 이것, R5. host 검증 필요) | **예.** 서비스의 ProgramArguments가 런처 → 런처가 링크 변경을 감지해 자식 교체. launchd 도메인을 건드리지 않음 |
| 부팅 시 코드 검증 | 별도 필요 | 별도 필요 | **런처가 매 기동·매 전환마다 서명·해시 검증** |
| 유지 비용 | 로그인 세션을 유지해야 해서 재부팅·FileVault와 충돌 | root 의존 유지 (목표에 반함) | 런처 코드 1개(작게)가 새 코어가 됨 |

**권장: C.** 이유는 (1) 로그인 없는 부팅 기동은 Daemon만 되고, (2) Daemon의 재시작에 root가 드는 문제를 launchd 도메인 밖에서 해결하며, (3) 같은 런처가 "실행 시점 검증"을 해서 `evedeploy`가 쓰는 디렉터리의 내용을 서비스 계정이 그냥 믿지 않게 해 주기 때문입니다.

런처의 책임(작게, 코어에 포함, root 소유 0755, 핀 sha256):

1. 기동 시 `live/gateway`가 가리키는 릴리스 디렉터리의 `MANIFEST` + `sigstore.json`을 **오프라인 `cosign verify-blob`** (root 소유 cosign·trust root, 신원 상수 포함) 으로 검증하고, 서명된 MANIFEST의 sha256과 바이너리를 대조합니다.
2. 버전이 floor 이상이고 blocklist에 없을 때만 자식(`gateway`)을 실행합니다. **TOCTOU 방지**: 검증한 파일을 `_evegw` 전용 실행 디렉터리로 복사해 그 사본을 해시한 뒤 실행합니다.
3. 자식이 사는 동안 `live/gateway` 링크 대상을 주기적으로(예: 2초) 확인합니다. 바뀌면 SIGTERM → 종료 대기 → 1부터 다시 합니다. 새 릴리스가 검증에 실패하면 **이전 링크로 돌아가 계속 서비스**하고 실패를 로그에 남깁니다.
4. 자식이 비정상 종료하면 런처도 비정상 종료하여 launchd `KeepAlive`와 `ThrottleInterval`을 그대로 씁니다.

주의: `built_at` 14일 제한은 **배포 시점 검사**(재생 방지)로 남기고, 런처의 기동 시 검증에는 적용하지 않습니다. 적용하면 한 달 뒤 재부팅에서 멀쩡한 서비스가 안 올라옵니다.

대안 D(폴백): root 소유의 별도 LaunchDaemon이 `WatchPaths`로 `live/` 변경을 감지해 `launchctl kickstart -k system/com.llmserving.gateway`만 실행. 입력을 받지 않는 고정 명령이라 위험은 작지만 "배포 경로에 root 코드가 남는다"는 점이 목표와 어긋납니다. 런처 방식이 host에서 막힐 때만 쓰자는 의견입니다.

## 4. 디렉터리 소유·권한 모델

계정(모두 숨김, 로그인 셸 없음, 비밀번호 없음, admin 아님, 서로 그룹을 공유하지 않음):

| 계정 | 역할 | 비고 |
|---|---|---|
| root | 최초 설치와 코어 변경만 | 일상 경로에서 사용하지 않음 |
| `evedeploy` | poller·fetch·검증·전환 | 이미 존재(uid 460-499) |
| `_evegw` (451) | gateway 실행 | 이미 존재 |
| `_evepg` | PostgreSQL 실행 (신규, §9) | |
| `_evemlx` | mlx_lm 실행 (신규, §9) | |
| `evebackup` | DB 백업 | 이미 존재, 아직 미사용 |
| 건영(owner) | 브레이크 해제 권한, 시크릿 회전 | admin이지만 일상 배포에는 불필요 |

`/opt/eve-gw` 레이아웃 제안 (mode는 `rwx` 8진수):

| 경로 | 소유 | 모드 | 내용 · 이유 |
|---|---|---|---|
| `/opt/eve-gw` | root:wheel | 0755 | 뼈대. 하위 소유 경계를 고정 |
| `core/` | root:wheel | 0755 (파일 0755/0644) | **코어**: `launcher`, `cosign`, `trusted_root.json`, 신원 상수. `evedeploy`·`_evegw` 모두 읽기만 |
| `releases/gateway/<ver>/` | evedeploy:evedeploy | 0755 (파일 0644, 실행 파일 0755) | 바이너리, `eve-gw`, `MANIFEST`, `sigstore.json`. `_evegw`는 world-read로 읽음 |
| `releases/web/<ver>/` | evedeploy:evedeploy | 0755 | 정적 UI 트리 (심볼릭 링크 금지 검사는 기존 `web_tree_clean` 유지) |
| `live/` | evedeploy:evedeploy | 0755 | 심볼릭 링크만: `gateway`, `web`, `previous-gateway`, `previous-web` |
| `run/` | _evegw:_evegw | 0750 | 런처가 검증 후 복사한 실행 사본, 소켓 |
| `etc/` | _evegw:_evegw | 0700 | `config.yaml`, `api_key`, `session_pepper`, 감사 HMAC 키, DB URL. 파일 0600. **`evedeploy`는 접근 불가** |
| `log/` | _evegw:_evegw | 0750 | 서비스 로그 |
| `state/` | §5.3 | | 브레이크와 버전 기록 |
| `/var/evedeploy/poll` | evedeploy | 0700 | poller 작업 디렉터리 (기존 그대로) |
| `staging/` | evedeploy | 0700 | 다운로드 스테이징. 위치는 `/var/evedeploy/`로 옮기는 것을 제안 (코어 디렉터리 밖) |

핵심 결과:
- **`evedeploy` 소유 영역은 `releases/`, `live/`, `staging/`, `poll/` 뿐**이고, 거기에 있는 코드는 서비스가 쓰기 전에 **런처가 서명으로 다시 확인**합니다.
- **코드가 `_evegw` 소유가 아닙니다.** 서비스가 자기 코드를 못 고친다는 4.0.0 규칙(runbook §0)의 의도를 "root 소유" 대신 "배포 계정 소유 + 실행 시점 서명 검증"으로 지킵니다. 실행 사본(`run/`)은 `_evegw` 소유라서 침해된 gateway가 사본을 고칠 수는 있지만, 다음 기동 때 런처가 `releases/`의 서명본으로 다시 만듭니다.
- 웹 정적 파일은 gateway가 `static_dir`로 요청마다 디스크에서 읽습니다(`ui_ok` 주석). `static_dir`을 `live/web`로 바꾸면 **UI 배포는 재시작이 필요 없고 링크 교체만**으로 끝납니다. 이 설정 변경은 gateway 설정(`config.yaml`) 변경이라 S2에 들어갑니다.
- 그룹 규칙: **`evedeploy`를 `_evegw`·`_evepg`·`_evemlx` 그룹에 넣지 않습니다.** 4.0.0 runbook의 "그룹 멤버십이 비어 있어야 함" 점검(`dscl . -read /Groups/_evegw GroupMembership`)을 모든 서비스 그룹으로 늘립니다.

## 5. 서명 검증과 롤백 유지

### 5.1 검증은 그대로, 실행 위치만 바꿉니다

기존 `eve-deploy`가 하던 검증을 같은 순서로 `evedeploy` 권한 스크립트가 합니다. **서명 신원·정책은 바뀌지 않습니다.**
- `cosign verify-blob`(Sigstore keyless, 고정 trust root, 오프라인), 인증서 신원 `deploy-sign.yml@refs/heads/main`, issuer, `push` 트리거, ref `refs/tags/vX.Y.Z`, `branch == main`, kind별 저장소 고정
- MANIFEST 엄격 파싱, 버전은 설치본보다 높아야 함(단조 증가), `built_at` ≤ 14일, tar·멤버 sha256, 아키텍처
- 적대적 tar 처리(`unpack_bundle`)와 웹 트리 검사(`web_tree_clean`)도 재사용

달라지는 점은 검증이 끝난 결과를 **root 디렉터리에 쓰는 대신 `releases/`에 푼다**는 것과, 같은 검증을 **런처가 기동 시 한 번 더** 한다는 것(§3)입니다. 검증 코드를 두 곳에 두지 않도록 `cosign` 호출·MANIFEST 해석은 코어의 작은 라이브러리로 합치는 방향을 권장합니다(DevOps와 협의, §12).

### 5.2 롤백

- 전환은 `live/gateway` → `releases/gateway/<new>` 링크 교체(`ln -sfn`로 임시 링크 만든 뒤 `mv -f`, 원자적), 이전 대상은 `live/previous-gateway`에 남깁니다. 웹도 같은 방식입니다.
- 롤백 = 링크를 이전으로 되돌리기. **sudo가 필요 없습니다.** 런처가 전환을 감지해 자식을 교체합니다. 보관 버전은 기존과 같이 **직전 1개**(`releases/`의 오래된 것은 정리)입니다.
- 자동 롤백: 배포 직후 헬스 5분 관찰(기존 `eve-poll observe`와 동일), 3회 연속 실패 시 링크 되돌림. 버전 blocklist와 PAUSE 래치는 §5.3.
- 스키마가 다르면 자동 롤백을 거부하는 기존 규칙(DB 다운마이그레이션 없음) 유지. 단 **현재 스키마 값은 지금 root 상태 파일(`schema`)인데, 새 모델에서는 `evedeploy`가 DB를 못 읽음**: MANIFEST의 스키마와 "마지막으로 헬스가 통과한 릴리스의 스키마"를 `state/`에 기록하는 방식을 제안합니다(§12 질문).

### 5.3 브레이크(PAUSE·blocklist·floor): 조이기는 배포 계정, 풀기는 소유자

지금은 모두 root 소유라서 호출자가 못 풉니다. 새 모델에서 이것을 `evedeploy`가 쓸 수 있는 곳에 두면 **침해된 `evedeploy`가 PAUSE를 지우고 floor를 낮추는 것**이 가능해집니다. 그래서 동작을 둘로 나눕니다.

| 동작 | 예 | 권한 |
|---|---|---|
| 조이기 (더 안전한 쪽) | PAUSE 생성, blocklist 추가, floor 올리기 | `evedeploy` 가능 |
| 풀기 (덜 안전한 쪽) | PAUSE 해제, blocklist 삭제, floor 내리기 | **`evedeploy` 불가.** 소유자 계정(건영, root 아님) 또는 root |

구현 후보(보안·DevOps와 확정, §12):
1. `state/`를 소유자 계정 소유(0755, `evedeploy`는 읽기만)로 두고, `evedeploy`는 별도의 추가 전용 디렉터리 `state/requests/`(`evedeploy`가 파일을 만들 수 있는 곳)에 "PAUSE 요청·blocklist 요청·floor 상향 요청"만 남깁니다. 검증기와 런처는 `state/`와 `requests/`를 **합쳐서** 읽되, 해제는 소유자 계정이 쓰는 `state/ack`로만 인정합니다(PAUSE는 "요청 건수 > ack 값"이면 정지). `evedeploy`는 `ack`를 쓸 수 없습니다. `requests/`에서 `evedeploy`가 파일을 지워 PAUSE를 우회하는 것은 막아야 하므로 "삭제 불가" 보장이 필요하며, 이것은 후보 2와 같은 ACL·플래그 확인에 걸립니다.
2. floor는 "올리기만" 하는 추가 전용 기록으로 두고, 유효 floor = 기록 중 최댓값. `evedeploy`가 지우는 것을 막으려면 macOS ACL(`add_file`만 허용)이나 파일 플래그가 필요하며, **ACL 동작은 host에서 확인이 필요**합니다.
3. 대안: floor·blocklist·PAUSE를 런처(`_evegw`)가 직접 소유하고, `evedeploy`는 요청 파일만 남기게 함. 구조는 단순하지만 침해된 gateway가 같은 uid라 brake를 고칠 수 있다는 약점이 있습니다(이미 `_evegw`가 시크릿을 쥔 상태이므로 추가 위험은 작음).

## 6. 위협 모델 요점

| 위협 | 현재 | 새 모델 | 잔여 위험 |
|---|---|---|---|
| CI 서명 파이프라인 침해 | 서명된 악성 릴리스가 root로 설치(검증기는 root) | 서명된 악성 릴리스가 **`_evegw`로** 실행 (root 아님) | 서비스 계정이 가진 것(시크릿·DB)은 노출. 신원 고정·태그 보호·14일·floor·PAUSE는 그대로 |
| `evedeploy` 침해 (poller·fetch·curl 응답·tar 해석의 버그) | root 승격 경로는 sudoers 4개 동사뿐이라 영향 제한 | `releases/`·`live/`를 쓸 수 있으나 **서명되지 않은 코드는 런처가 거부**. 시크릿·DB 접근 없음 | 서비스 거부(링크를 깨뜨림), 서명된 **이전** 릴리스로의 되돌림(floor 위, blocklist 밖일 때) |
| sudoers 규칙 침해 | 있음 (4개 동사) | **규칙 자체가 없음** | 없음 |
| 코어 변조 | root 소유 | root 소유 | 변경은 사람 root 1회. 핀 sha256 + 보안 리뷰 |
| 검증 후 교체(TOCTOU) | root 전용 작업 디렉터리에서 검증 | 런처가 `run/` 사본을 만들고 그 사본을 해시한 뒤 실행 | `_evegw`가 침해되면 사본 변경 가능, 재기동 시 복구 |
| 서비스 계정 상승 | `_evegw`는 코드 수정 불가 | `_evegw`가 `run/`을 수정 가능 (코드는 `releases/`에 서명본으로 남음) | 약간 약해진 부분. 런처 재검증으로 지속성 차단 |
| 시크릿 | `etc/` `_evegw` 0600 | 동일. `evedeploy` 접근 없음 | 동일 |
| 시계 오차 | `built_at` 검사 실패 → 거부 | 동일 (배포 시점에만) | 안전한 방향 |
| 시스템 전반 | 배포가 root 코드 실행 경로 | **배포 경로에 root 코드 없음** | 최초 설치와 코어 변경 때의 사람 root |

정리: **강해지는 점**은 배포 경로에서 root 코드 실행이 없어진다는 것(침해 시 최대 영향이 root → `_evegw`로 내려감). **약해지는 점**은 root 전용 검증 디렉터리의 방어가 사라지고, 그 몫을 런처 재검증과 소유 경계가 대신한다는 것입니다. 이 균형이 맞는지가 보안 담당의 핵심 판단 사항입니다.

## 7. DevOps 초안(티켓)과의 정합

- 코어/페이로드 분리: **동의.** 코어 = root 소유(§4 `core/`), 페이로드 = `evedeploy` 소유(poller, fetch, 헬퍼, 헬스체크, 릴리스).
- 페이로드를 `kind=tools` 서명 릴리스로 자동 갱신: **동의**, 단 적용 주체는 root 엔트리포인트가 아니라 `evedeploy`. 그래서 초안의 "sudo 동사는 늘리지 않는다"는 "**sudoers 동사 자체가 없다**"로 더 강해집니다.
- 초안 §6 남는 위험 "서명된 악성 페이로드가 root로 실행": 이 모델에서는 사라집니다. 초안 §7 결정 1(적용 지연)은 **필수가 아니라 선택**이 됩니다(영향이 `evedeploy` 범위라서).
- 초안 §5 일정: 이번 한 줄 명령(재설치)은 그대로 두고, **"마지막 사람 sudo 1회"를 이 문서의 S2로 정의**합니다. 코어를 작게 만든 뒤 한 번에 깔 수 있게 S0·S1을 먼저 둡니다.
- PR #168(one-shot 단계 추가)은 현 모델(sudoers 기반) 위의 변경입니다. 이 설계가 확정될 때까지 그대로 진행해도 되며, **S3에서 제거되는 것**(sudoers 설치, `rehearse/unpause`의 sudo 경로)은 #168이 추가하는 단계와 겹치지 않게 순서를 맞춥니다.

## 8. 단계별 이행 계획과 롤백, SemVer 판정

모든 단계는 자기 롤백 방법을 갖고, 앞 단계가 끝나 헬스·리허설이 통과한 뒤에만 다음으로 갑니다. **보안 리뷰 통과 전에는 S2 이후를 시작하지 않습니다.**

| 단계 | 내용 | root | SemVer | 롤백 |
|---|---|---|---|---|
| S0 | 이 설계 확정(보안·DevOps 리뷰). 코드 변경 없음 | 없음 | PATCH (문서만) | PR 되돌리기 |
| S1 | `evedeploy` 권한 검증기·전환 스크립트를 **드라이런 전용**으로 추가. 현 sudo 경로와 같은 입력으로 결과를 비교하는 테스트(샌드박스, 실제 서명 픽스처 재사용). host 동작 변화 없음 | 없음 | MINOR (추가만) | 파일 삭제 |
| S2 | **마지막 root 설치**: `core/`(런처·cosign·trust root), `releases/`·`live/`·`state/`·`run/` 디렉터리, 새 plist(런처 실행), `static_dir`을 `live/web`로, 기존 `/opt/eve-gw/bin`·`web`은 **그대로 남겨 두고** 병행 설치. 전환은 plist 교체 1회 | **1회** | **MAJOR** (보안 경계, 서비스 실행 경로, 설정 키 변경) | 이전 plist 복원 + 이전 `bin`·`web`이 남아 있어 즉시 복귀. 이 롤백은 root 필요(설치 단계의 롤백이므로 허용) |
| S3 | poller를 sudo 없는 경로로 전환, **sudoers 규칙 제거**, `rehearse`(일부러 실패하는 릴리스)로 자동 롤백·브레이크 검증, 소유자 계정 해제 경로 검증 | 규칙 삭제 1회 (또는 S2에 포함) | **MAJOR** (권한 모델 변경) | 규칙 재설치(root)로 구 경로 복귀. 구 엔트리포인트는 S4까지 남겨 둠 |
| S4 | 구 root 엔트리포인트·`uninstall` 정리, 런북·README·`eve-web/.github/workflows/release.yml` 머리말에서 `sudo` 문구 제거, `scripts/test`에서 sudo 테스트 정리 | 없음 | **MAJOR** (운영 인터페이스 삭제) | 이전 태그로 되돌림 |
| S5 (선택, 별도 PR) | PostgreSQL을 `_evepg`, mlx_lm을 `_evemlx` LaunchDaemon으로 분리, 백업을 `evebackup`으로 | **1회** (계정·데이터 이동) | **MAJOR** (데이터 소유 변경). **데이터 이동·삭제는 건영님 직접 승인** | `pg_dump` 백업 복원, 기존 데이터 디렉터리는 확인될 때까지 삭제하지 않음 |

버전 선: 이 변경은 **#158(6.0.0)에 묶지 않습니다.** 어느 MAJOR 번호에 싣는지는 PM이 정합니다. 배포 도구는 gateway와 별개 버전 선이라 도구만 올리는 방법도 가능하지만, 불확실하므로 MAJOR로 둡니다.

각 단계 공통 완료 조건: (1) CI 통과, (2) 보안 리뷰, (3) S2·S3·S4·S5는 건영님 직접 확인, (4) 샌드박스 테스트에 "서명 실패·변조·다운그레이드·blocklist·PAUSE 해제 시도"가 포함됨.

## 9. 계정·권한 분리 (PostgreSQL, mlx_lm, 서명·시크릿 키)

배포 계정이 키나 DB 데이터를 **읽거나 쓸 수 없게** 하는 것이 요구사항입니다. 표는 최종 목표이며, 현재는 H1~H3처럼 건영님 계정 아래에 있습니다.

| 자원 | 소유 계정 | 경로·모드 | `evedeploy` | `_evegw` | 비고 |
|---|---|---|---|---|---|
| PostgreSQL 데이터 디렉터리 | `_evepg` | `_evepg:_evepg 0700` | 접근 없음 | 접근 없음(TCP loopback으로만 접속) | 현재는 brew 접두 아래 건영님 계정. 이전 방법은 S5 |
| DB 역할 `llmchat` 비밀번호 | `_evegw` | `etc/` 안, 0600 | 접근 없음 | 읽기 | DDL은 gateway가 기동 시 자동 마이그레이션(`GW_DB_AUTO_MIGRATE`)으로 수행 → **배포 계정이 DB에 닿을 필요 없음** |
| DB 접속 방식 | | `127.0.0.1` scram, 소켓 peer | | | `pg_hba.conf`는 `_evepg` 소유(데이터 디렉터리 안). `evedeploy`는 접근 불가 |
| 백업 | `evebackup` | `evebackup:evebackup 0700` 백업 디렉터리 | 접근 없음 | 접근 없음 | DB 역할 `evebackup`에 `pg_read_all_data`만 부여 + peer 매핑. 현재는 건영님 계정이 슈퍼유저 peer로 실행 |
| mlx_lm 실행 | `_evemlx` | LaunchDaemon `UserName=_evemlx`, 포트 `127.0.0.1:8080` | 접근 없음 | 포트로만 호출 | 모델 파일은 미리 내려받고 서비스는 오프라인 모드(`HF_HUB_OFFLINE=1` 등)로 실행하는 방안 |
| mlx_lm venv·코드 | root | root:wheel 0755 (서비스가 쓰기 불가) | 접근 없음 | 접근 없음 | 패키지 업그레이드는 **gateway 배포 범위 밖**(코어와 같은 사람 root 작업) |
| 모델 가중치 | root 또는 `_evemlx` | 읽기 전용 | 접근 없음 | 접근 없음 | 크기가 크므로 별도 디렉터리 |
| API 키·세션 페퍼·감사 HMAC 키 | `_evegw` | `etc/` 0700, 파일 0600 | **접근 없음** | 읽기·쓰기 | 4.0.0 runbook §2·§9를 그대로. 회전 절차는 §10 |
| 서명 키 | **없음** | - | - | - | Sigstore keyless라 보관할 서명 비밀이 없음(`docs/deploy-signing.md`) |
| 릴리스 태그 deploy key | 건영(owner) | `~/.eve-release-tagger/id_ed25519` 0600 | **접근 없음** | 접근 없음 | 서버에 둘 필요가 없으면 서버에서 제거를 검토(보안 담당) |
| trust root·cosign·런처·신원 상수 | root | `core/` | 읽기만 | 읽기만 | 핀 sha256 |
| 일회용 임시 토큰 (one-shot) | 건영(owner) | 환경 변수에서만 | 없음 | 없음 | PR #168 조건 그대로 |

점검 항목(비root로 확인 가능한 것만): 각 디렉터리 `ls -ld` 소유·모드, 서비스 그룹 멤버십 비어 있음, `sudo -n -l -U evedeploy` 에 NOPASSWD 없음(S3 이후).

## 10. 시크릿 회전은 어떻게 하나

R13의 `sudo -e etc/config.yaml`, api_key 교체는 지금 root입니다. 소유자 계정이 `etc/`에 접근해야 하는데 `etc/`는 `_evegw` 0700입니다. 후보:
- (a) 회전 도구를 `_evegw` 권한 서브커맨드로 제공(예: `gateway rotate-key`). 소유자 계정이 `_evegw` 전용 로컬 관리 소켓(0600)으로 요청. gateway 코드 변경이 필요해 별도 설계.
- (b) 회전은 root 작업으로 남기고 **코어 변경과 같은 예외**로 취급.
이번 범위(배포)에는 포함하지 않고 (b)를 기본으로 둡니다. 소유자 sudo 제로 목표와는 별개 티켓으로 두자는 의견입니다.

## 11. 하지 않는 것 (범위 밖)

- gateway 요청 경로·API 계약·툴 선택 정책 변경 없음
- 서명 신원 정책 변경 없음(§5.1에서 그대로)
- 데이터 이동·삭제 없음(S5는 별도 PR과 별도 승인)
- 실제 host 이름·주소·시크릿은 이 문서에 쓰지 않음(필요 시 `<TS_HOST>`)

## 12. DevOps·보안 담당에게 묻는 열린 질문

### DevOps (host에서 확인이 필요한 사실)

1. 로그인하지 않은 숨김 서비스 계정으로 `launchctl kickstart system/<label>`이 **실제로 거부되는지** (R5 가정). 같은 계정의 `launchctl print system/<label>`은 읽을 수 있는지.
2. LaunchAgent가 로그인 없는 서비스 계정에서 부팅 때 **올라오지 않는 것**(§3의 A 부정)을 실제로 확인할 수 있는지. 시도해 보고 결과만 남겨 주세요.
3. 런처 구현 형태: bash(3.2) vs Go 단일 바이너리. 배포 코드와 같은 언어로 테스트하기 쉬운 쪽, 그리고 코어에 들어갈 코드 크기.
4. 런처 감시 방식: 링크 폴링(2초) vs 다른 방법. 롤백·전환의 재시작 지연이 허용 범위인지(기존 `kickstart -k`보다 느린지).
5. 루트 소유 `WatchPaths` 폴백(대안 D)을 준비할지.
6. `_evemlx`로 mlx_lm을 LaunchDaemon으로 돌릴 때 Metal(GPU) 접근이 문제없는지. 모델 오프라인 모드 가능 여부.
7. 재부팅 시 FileVault가 켜져 있으면 로그인 전에 Daemon이 올라오는지(현재 운영 방식 확인).
8. 현재 PostgreSQL(brew services)을 `_evepg`로 옮길 때의 다운타임과 데이터 이동 방법. 이 작업을 S5로 분리하는 것이 맞는지.
9. `tailscale serve`와 pf 설정(H5, R14)을 root 없이 유지할 수 있는지(예: `tailscale set --operator=` 최초 1회).
10. 현재 스키마 값(`schema`)을 `evedeploy`가 어떻게 알 것인가: MANIFEST 스키마 + "마지막 헬스 통과 릴리스" 기록 방식으로 충분한지.
11. PR #168과 순서: #168을 먼저 머지하고 S1부터 이어가도 문제가 없는지. `setup-pim-adapter.sh`가 어떤 계정으로 무엇을 설치하는지(이 문서는 #168의 `setup-pim-adapter.sh`를 읽지 못했습니다).

### 보안 담당 (판단이 필요한 것)

1. **모델 자체**: "root 전용 검증 디렉터리" 대신 "`evedeploy` 소유 + 런처 실행 시점 검증"으로 바꾸는 것이 위협 모델상 허용되는가(§6의 강해지는 점/약해지는 점). 거부한다면 대안 D(root 소유 재시작기)와 뭐가 더 필요한가.
2. 브레이크(PAUSE·blocklist·floor)의 저장 위치와 "풀기" 권한을 소유자 계정으로 두는 방식(§5.3 후보 1~3 중 무엇).
3. 런처 검증 범위: 서명 + 해시 + floor + blocklist로 충분한가. 기동 시 `built_at`을 검사하지 않는 것이 맞는가(§3).
4. TOCTOU 대응으로 `run/` 사본을 쓰는 것이 충분한가. `evedeploy`가 침해되어 서명된 이전 릴리스로 되돌리는 공격(floor 위 가장 낮은 버전)의 허용 범위.
5. `_evegw`가 `run/`을 쓸 수 있어 "서비스가 자기 코드를 수정하지 못한다"는 4.0.0 규칙이 약해지는 것을 허용하는가.
6. 코어 변경은 항상 사람 root라는 예외(티켓 §7 결정 4)를 받아들이는가. 코어 변경의 리뷰 요건(핀, 리허설).
7. 릴리스 태그 deploy key를 서버에 둘 필요가 있는지(현재는 건영님 홈).
8. DevOps 초안의 "적용 지연"은 이 모델에서 필수인가 선택인가.
9. 시크릿 회전을 root 예외로 남기는 안(§10 (b))의 허용 여부.
10. 소유자 계정을 admin으로 두는 현 상태가 §5.3의 "풀기 권한"에 적합한가(소유자 계정이 침해되면 풀 수 있음).

## 13. 승인과 리뷰 상태

- 보안 리뷰: **필수, 미실시.** 이 문서가 머지되어도 S2 이후는 보안 승인 없이 진행하지 않습니다.
- 건영님 직접 확인: S2·S3·S4·S5(모두 MAJOR). 데이터 이동·삭제와 비용·외부 노출 변경이 생기면 별도로 직접 승인.
- 이 문서의 확정 순서: DevOps 초안과 정합 → 보안 리뷰 → PM 결정.
