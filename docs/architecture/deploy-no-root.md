# 배포에서 root 제거 설계 (root는 최초 설치에서만)

> 상태: **설계 제안 (rev 2, 보안 리뷰 반영, 미구현)** · 작성: 시스템 아키텍트 · 티켓: Notion `[OPS] 건영 sudo 제로화: 배포 도구 자가 갱신 설계` (담당 DevOps, 같은 티켓에 DevOps 초안과 아키텍트 의견 절이 따로 있음) · 관련 PR: #168(one-shot 단계 추가, DevOps)
>
> **SemVer 판정: MAJOR 예상. 보안 경계(root 소유 검증기 → 서비스 계정 권한)가 바뀌므로 보안 담당 리뷰가 필수입니다.** 불확실한 항목은 모두 MAJOR로 둡니다. MAJOR 배포는 건영님이 직접 확인한 뒤에만 합니다. 이 문서는 설계만 담고, 코드·배포·host 변경은 포함하지 않습니다.
>
> **rev 2**: 보안 담당 리뷰(대상 head `a8fb855`)에서 제3안이 채택됐고, 조건·High 1건·Med 2건·문구 정정을 반영했습니다(§13에 항목별 반영 위치).

## 0. 요약

- 지금은 배포 때마다 `sudo`가 한 점 있습니다. 서명 검증·파일 교체·서비스 재시작이 모두 **root로 도는 진입점 하나**(`/usr/local/libexec/eve/deploy`)에 모여 있고, sudoers의 NOPASSWD 4개 동사(`deploy`, `deploy-web`, `rollback`, `rollback-web`)가 그 문을 엽니다.
- 목표: **root는 최초 설치(1회)에서만** 쓰고, 이후 배포·롤백은 서비스 계정(`evedeploy`)만으로 합니다. sudoers 규칙은 없앱니다.
- 핵심 설계 결정 6가지 (rev 2)
  1. **root 소유는 "신뢰의 뿌리"로 한정하고 한 번만 설치**: cosign, trust root, 신원 상수, 런처, env allow-list, launchd plist. 이것들과 **그 상위 디렉터리까지 전부 root 소유 경로 사슬**이어야 합니다(§3.1). 이것이 바뀌는 일(= 코어 변경)만 사람 root입니다.
  2. **바이너리·정적 파일은 `evedeploy`가 소유**하는 `releases/` 아래 버전 디렉터리에 두고, `live/` 심볼릭 링크는 **"이 릴리스를 적용해 달라"는 신호**로만 씁니다. `live/`·`releases/` 안의 내용은 신뢰 근거가 아닙니다.
  3. **서비스는 LaunchDaemon + `UserName=_evegw`**, **런처도 root가 아닌 `_evegw`로 실행**합니다. 재시작은 sudo 없이 **런처가 링크 변경을 감지해 자식 프로세스를 교체**합니다. **실행을 막는 게이트는 런처 하나**이고, `evedeploy`의 배포 시점 검증은 보조입니다. 런처는 복사본에서 배포 시점 검증과 **같거나 더 엄격하게** 검증합니다(§3.2).
  4. **웹 정적 파일도 같은 게이트를 지납니다**: `static_dir`은 `live/web`이 아니라 **`run/web`(런처가 검증한 복사본)** 입니다(§4).
  5. **롤백 대상은 런처 소유의 `last_good`** 입니다. `live/previous-*` 링크를 바꿔 끼워도 롤백 대상이 바뀌지 않습니다(§5.2). 브레이크는 `evedeploy`가 쓸 수 있는 곳의 ACL 가정에 기대지 않습니다(§5.3).
  6. **보안 관련 env 키는 root 소유 allow-list로** 통제합니다. `_evegw` 소유 파일로 옮기는 안은 폐기했습니다(§7).
- **rev 1 문구 정정**: rev 1은 "`evedeploy`가 뚫려도 서명되지 않은 코드는 서비스 계정으로 실행되지 않습니다"라고 적었습니다. 이 문구는 **웹 정적 파일에 대해서는 사실이 아니었습니다**: `static_dir=live/web`이면 gateway가 `evedeploy` 소유 디렉터리를 요청마다 직접 읽어, 런처 검증을 거치지 않습니다. 정정한 주장은 다음과 같습니다.
  - **gateway 바이너리**: 런처가 root 체인의 신뢰 입력으로 검증한 사본만 실행합니다(S2 설치 후).
  - **웹 정적 파일**: 런처가 `run/web` 사본을 만들어 검증하고 `static_dir`이 `run/web`을 가리킬 때(S2 설치 후)부터 같은 주장이 성립합니다. **그 전까지(S0~S1과 S2 전환 전) "서명 없는 코드는 실행 안 됨"이라고 쓰지 않습니다.**
  - 두 경우 모두 "서비스 계정이 가진 시크릿·DB는 노출될 수 있다"는 잔여 위험은 그대로입니다(§6).
- DevOps 초안 v2(티켓, "서비스 계정 권한만으로 배포")와 방향이 같습니다(감시 wrapper 방식 R1, `releases/` + 링크 교체, 최초 1회 root). 이 문서가 더하는 것은 **검증 로직의 소유 문제**(초안 v2 보안 질문 1)에 대한 제3안입니다: 신뢰의 뿌리는 root 소유로 1회 설치하고, `evedeploy`는 그것을 **호출만** 하며, **런처가 실행 시점에 한 번 더(그리고 최종으로) 검증**합니다(§3, §5). 이 안은 보안 리뷰에서 채택됐습니다(§13).

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
| H6 | 캘린더 어댑터 `/opt/eve-pim` (root 소유, 사용자 `gui` LaunchAgent, TCC) | 티켓(DevOps 초안 v2). 이 문서 범위 밖 |

## 2. 최초 설치(root)와 일상 배포(서비스 계정)의 경계

원칙: **"무엇을 신뢰할지 정하는 것"은 root가 1회, "신뢰된 것을 적용하는 것"은 서비스 계정이 매번**.

| 구분 | root (최초 1회, 이후 코어 변경 때만) | `evedeploy` (일상 배포) |
|---|---|---|
| 계정 | 계정 생성(R9), 그룹 규칙 | - |
| 코어 파일 | cosign, `trusted_root.json`, 런처, plist, env allow-list, 핀 sha256 (상위 디렉터리 포함 root 소유 사슬) | 읽기만 |
| 디렉터리 | `/opt/eve-gw` 뼈대와 소유 지정 | `releases/`, `live/`, 스테이징 안에서만 |
| 서비스 등록 | LaunchDaemon plist 설치·로드 | 없음 |
| 서명 검증 | (검증기 자체를 설치) | fetch → 검증(서명·신원·버전·해시·built_at) → 풀기. **보조 검증**: 최종 게이트는 런처 |
| 전환·롤백 | - | `live/` 링크 교체(적용 요청), 헬스 관찰. **롤백 대상은 런처의 `last_good`** (`evedeploy`는 롤백 요청만) |
| 브레이크 | 초기값(floor 하한) 생성, **풀기**(§5.3) | **조이기 요청만**(§5.3) |
| 시크릿 | 최초 생성 | 접근 없음 |

**코어 변경은 계속 사람 root입니다**(Sigstore trust root 회전, cosign 교체, 런처 수정, 신원 상수 변경). 그래서 코어를 작게 유지하고 변경 때마다 보안 리뷰·핀·롤백 리허설을 거칩니다. 이것은 티켓 §7 결정 4("코어 변경은 사람이라는 예외를 받아들일지")의 아키텍트 답이며, **받아들이는 것을 권장합니다**.

## 3. 서비스 형태: LaunchAgent vs LaunchDaemon + UserName

사실과 아직 확인하지 못한 것을 구분해서 적습니다. 이 메모를 쓴 환경은 macOS host가 아니라서 **launchd 동작은 host에서 검증해야 하는 항목**으로 둡니다(§12).

| 기준 | A. LaunchAgent (서비스 계정) | B. LaunchDaemon + `UserName` + sudo `kickstart` (현재) | **C. LaunchDaemon + `UserName` + 런처가 전환 (권장, 보안 리뷰 채택)** |
|---|---|---|---|
| 로그인 없이 부팅 시 기동 | **아니오.** Agent는 사용자 세션(`gui/<uid>` 또는 `user/<uid>` 도메인)이 있을 때 올라옴. 로그인하지 않는 숨김 서비스 계정은 세션이 없음 (host 검증 필요) | **예.** 레포가 이미 이 방식을 씀: poller plist 주석 "A LaunchDaemon: no login needed" (`com.llmserving.evepoll.plist.template`), gateway도 4.0.0 runbook에서 `/Library/LaunchDaemons` + `UserName=_evegw` | **예** (B와 같음) |
| sudo 없이 재시작 | 같은 계정이 자기 도메인의 서비스를 `kickstart`할 수는 있으나, 세션이 없으면 도메인 자체가 없음 | **아니오.** system 도메인 `kickstart`는 root (`svc_restart`가 바로 이것, R5. host 검증 필요) | **예.** 서비스의 ProgramArguments가 런처 → 런처가 링크 변경을 감지해 자식 교체. launchd 도메인을 건드리지 않음 |
| 부팅 시 코드 검증 | 별도 필요 | 별도 필요 | **런처가 매 기동·매 전환마다 서명·해시 검증** |
| 런처 실행 계정 | - | - | **`_evegw` (root 아님)** |
| 유지 비용 | 로그인 세션을 유지해야 해서 재부팅·FileVault와 충돌 | root 의존 유지 (목표에 반함) | 런처 코드 1개(작게)가 새 코어가 됨 |

**권장: C.** 이유는 (1) 로그인 없는 부팅 기동은 Daemon만 되고, (2) Daemon의 재시작에 root가 드는 문제를 launchd 도메인 밖에서 해결하며, (3) 같은 런처가 "실행 시점 검증"을 해서 `evedeploy`가 쓰는 디렉터리의 내용을 서비스 계정이 그냥 믿지 않게 해 주기 때문입니다.

### 3.1 신뢰 경로 사슬 (보안 리뷰 조건 1)

**런처와 런처가 신뢰 판단에 읽는 모든 파일·plist는 상위 디렉터리까지 전부 root 소유 경로로 이어져야 합니다.**

- 대상: 런처 실행 파일(과 런처가 bash 등 스크립트라면 그 인터프리터가 읽는 모든 파일), `cosign`, `trusted_root.json`, 신원 상수, **env allow-list**(§7), floor 하한(§5.3), `/Library/LaunchDaemons`의 gateway plist.
- 조건: 경로의 **모든 구성 요소**(`/`, `/opt`, `/opt/eve-gw`, `core/`, 파일 자체, plist의 상위 `/Library`, `/Library/LaunchDaemons`)가 `root:wheel`이고 group·other 쓰기 비트가 없고, 심볼릭 링크가 아닙니다.
- 런처는 시작할 때 이 사슬을 구성 요소마다 `lstat`로 **스스로 검사하고, 어긋나면 자식을 띄우지 않고 종료**합니다(fail closed). 설치 스크립트와 점검 항목(§9)에도 같은 검사를 둡니다.
- 런처는 임시 파일을 `TMPDIR` 등 공용 위치가 아니라 `run/tmp/`(`_evegw` 0700)에만 둡니다.
- **구분**: 신뢰 판단에 쓰는 입력은 위 root 사슬에서만 읽습니다. **검증 대상 페이로드**(`releases/` 안의 파일)와 `evedeploy`가 남기는 요청 파일(§5.3)은 **신뢰하지 않는 입력**으로 취급해, 엄격 파싱·검증을 통과한 것만 씁니다. 이 구분이 조건의 의도와 맞는지는 보안 담당 확인 항목입니다(§12).
- **런처는 root가 아닌 `_evegw`로 실행**합니다(plist `UserName=_evegw`). 침해돼도 root가 아니고, 런처가 쓰는 곳은 `run/`·`state/`뿐입니다.

### 3.2 런처의 책임 (게이트는 런처 하나)

작게, 코어에 포함, root 소유 0755, 핀 sha256. **`evedeploy`가 배포 시점에 하는 검증은 보조**이고, 이것이 통과했다는 사실은 런처가 신뢰하지 않습니다.

1. 기동·전환 때 `live/gateway`가 가리키는 릴리스 디렉터리의 내용을 먼저 **`run/`의 사본으로 복사**(TOCTOU 방지: 이후 검증과 실행은 모두 사본만)하고, 사본에 대해 아래를 검증합니다. **배포 시점 검증(§5.1)과 같거나 더 엄격**해야 하고, 더 느슨한 항목이 있어서는 안 됩니다.
   - 오프라인 `cosign verify-blob`(root 사슬의 cosign·trust root·신원 상수): 인증서 신원·issuer·`push` 트리거·ref `refs/tags/vX.Y.Z`·`branch == main`·kind별 저장소 고정
   - MANIFEST 엄격 파싱, 서명된 MANIFEST의 sha256과 사본 대조(바이너리·웹 트리 모든 멤버), 아키텍처, 웹 트리 검사(`web_tree_clean`: 링크·특수 파일 금지)
   - 버전이 **floor 이상, blocklist 밖, 그리고 현재 `last_good`보다 높을 것**(단조 증가). 배포 시점에는 설치본과의 비교였는데, 런처는 자기가 승격한 `last_good`과 비교합니다.
   - 새로 적용하는(아직 승격되지 않은) 릴리스에는 `built_at` ≤ 14일(재생 방지)도 적용합니다. 배포 시점과 같은 조건입니다.
2. 이미 승격되어 `last_good`인 릴리스를 **재기동·재부팅으로 다시 띄울 때는 `built_at`을 적용하지 않습니다**(한 달 뒤 재부팅에서 멀쩡한 서비스가 안 올라오지 않도록). 그 대신 사본을 다시 해시하고 서명을 다시 검증합니다. 이것이 "같거나 더 엄격"과 어긋나는지는 보안 담당 확인 항목입니다(§12).
3. 검증을 통과하면 **gateway 사본과 `run/web` 사본을 만들고** 자식(`gateway`)을 **정리된 환경변수**(§7)로 실행합니다.
4. 자식이 사는 동안 `live/gateway`·`live/web` 링크 대상을 주기적으로(예: 2초) 확인합니다. 바뀌면 1부터 다시 합니다(gateway는 SIGTERM → 종료 대기 → 교체, 웹은 `run/web` 링크를 원자적으로 교체하고 재시작 없음). 새 릴리스가 검증에 실패하면 **`last_good`으로 계속 서비스**하고 실패를 로그에 남깁니다.
5. 새 릴리스를 적용한 뒤 5분 동안 자식이 비정상 종료 없이 살아 있고 `evedeploy`의 롤백 요청이 없으면 그 릴리스를 `last_good`으로 **승격**합니다(§5.2). 자식이 비정상 종료하면 런처도 비정상 종료하여 launchd `KeepAlive`와 `ThrottleInterval`을 그대로 씁니다. 승격 전에 반복 종료하면 `last_good`으로 되돌립니다.

대안 D(폴백): root 소유의 별도 LaunchDaemon이 `WatchPaths`로 `live/` 변경을 감지해 `launchctl kickstart -k system/com.llmserving.gateway`만 실행. 입력을 받지 않는 고정 명령이라 위험은 작지만 "배포 경로에 root 코드가 남는다"는 점이 목표와 어긋납니다. 런처 방식이 host에서 막힐 때만 쓰자는 의견입니다. 보안 담당은 제3안(C)을 채택했으므로 폴백은 C가 host에서 막힐 때에 한정합니다.

## 4. 디렉터리 소유·권한 모델

계정(모두 숨김, 로그인 셸 없음, 비밀번호 없음, admin 아님, 서로 그룹을 공유하지 않음):

| 계정 | 역할 | 비고 |
|---|---|---|
| root | 최초 설치와 코어 변경만 | 일상 경로에서 사용하지 않음 |
| `evedeploy` | poller·fetch·검증(보조)·전환 요청 | 이미 존재(uid 460-499). **쓰기 범위는 `releases/`·`live/`·`staging/` 뿐** |
| `_evegw` (451) | 런처와 gateway 실행 | 이미 존재. 런처도 이 계정(root 아님) |
| `_evepg` | PostgreSQL 실행 (신규, §9) | |
| `_evemlx` | mlx_lm 실행 (신규, §9) | |
| `evebackup` | DB 백업 | 이미 존재, 아직 미사용 |
| 건영(owner) | 브레이크 풀기, 시크릿 회전 | admin이지만 일상 배포에는 불필요 |

`/opt/eve-gw` 레이아웃 제안 (mode는 `rwx` 8진수):

| 경로 | 소유 | 모드 | 내용 · 이유 |
|---|---|---|---|
| `/opt/eve-gw` | root:wheel | 0755 | 뼈대. 하위 소유 경계를 고정 |
| `core/` | root:wheel | 0755 (파일 0755/0644) | **코어**: `launcher`, `cosign`, `trusted_root.json`, 신원 상수, **env allow-list**, **floor 하한**. 상위 디렉터리 포함 root 사슬(§3.1). `evedeploy`·`_evegw` 모두 읽기만 |
| `releases/gateway/<ver>/` | evedeploy:evedeploy | 0755 (파일 0644, 실행 파일 0755) | 바이너리, `eve-gw`, `MANIFEST`, `sigstore.json`. **신뢰하지 않는 입력**: 런처가 사본을 만들어 검증한 뒤에만 사용 |
| `releases/web/<ver>/` | evedeploy:evedeploy | 0755 | 정적 UI 트리. **신뢰하지 않는 입력**(런처가 `run/web`으로 검증 복사) |
| `live/` | evedeploy:evedeploy | 0755 | 심볼릭 링크만: `gateway`, `web`. **적용 요청 신호**일 뿐 신뢰 근거가 아님. `previous-*` 링크는 **두지 않음**(롤백 대상은 `last_good`, §5.2) |
| `run/` | _evegw:_evegw | 0750 | 런처가 검증 후 복사한 실행 사본: `gw/`(gateway 사본), **`web/`(웹 정적 파일 사본, gateway의 `static_dir`)**, `last_good/`·`prev_good/`(서명 검증 완료본과 기록), `tmp/`, 소켓 |
| `etc/` | _evegw:_evegw | 0700 | `config.yaml`, `api_key`, `session_pepper`, 감사 HMAC 키, DB URL. 파일 0600. **`evedeploy`는 접근 불가**. 보안 관련 env 키는 두지 않음(§7) |
| `log/` | _evegw:_evegw | 0750 | 서비스 로그 |
| `state/` | _evegw:_evegw | 0755 (파일 0644, 비밀 없음) | 런처가 쓰는 상태: 현재 버전, `last_good`·`prev_good` 기록(버전·MANIFEST sha256·스키마), PAUSE 래치, floor·blocklist 기록. `evedeploy`는 읽기만(§5.3) |
| `/var/evedeploy/poll` | evedeploy | 0700 | poller 작업 디렉터리 (기존 그대로) |
| `staging/` | evedeploy | 0700 | 다운로드 스테이징과 `requests/`(조이기·롤백 요청, §5.3). 위치는 `/var/evedeploy/`로 옮기는 것을 제안 (코어 디렉터리 밖) |

핵심 결과:
- **`evedeploy` 소유 영역은 `releases/`, `live/`, `staging/`, `poll/` 뿐**이고, 거기에 있는 코드는 서비스가 쓰기 전에 **런처가 서명으로 다시 확인**합니다. 상수·런처를 바꾸거나 우회하는 경로는 이 범위에 없습니다.
- **코드가 `_evegw` 소유가 아닙니다.** 서비스가 자기 코드를 못 고친다는 4.0.0 규칙(runbook §0)의 의도를 "root 소유" 대신 "배포 계정 소유 + 실행 시점 서명 검증"으로 지킵니다. 실행 사본(`run/`)은 `_evegw` 소유라서 침해된 gateway가 사본을 고칠 수는 있지만, 다음 기동·전환 때 런처가 `releases/`의 서명본(또는 `last_good`)으로 다시 만들고 다시 검증합니다.
- **웹 정적 파일은 `run/web`에서 서빙합니다** (보안 리뷰 High). rev 1은 `static_dir=live/web`이라 `evedeploy`가 쓴 파일을 gateway가 검증 없이 읽었습니다. 이제 `static_dir`은 `run/web`이고, 런처가 `releases/web/<ver>`를 사본으로 만들어 검증(서명·해시·`web_tree_clean`)한 뒤 `run/web` 링크를 원자적으로 교체합니다. gateway는 `static_dir`을 요청마다 디스크에서 읽으므로(`ui_ok` 주석) **UI 배포는 재시작이 필요 없습니다**. 이 설정 변경은 gateway 설정(`config.yaml`) 변경이라 S2에 들어갑니다.
- 그룹 규칙: **`evedeploy`를 `_evegw`·`_evepg`·`_evemlx` 그룹에 넣지 않습니다.** 4.0.0 runbook의 "그룹 멤버십이 비어 있어야 함" 점검(`dscl . -read /Groups/_evegw GroupMembership`)을 모든 서비스 그룹으로 늘립니다.

## 5. 서명 검증과 롤백 유지

### 5.1 검증은 그대로, 실행 위치와 최종 게이트만 바꿉니다

기존 `eve-deploy`가 하던 검증을 같은 순서로 `evedeploy` 권한 스크립트가 합니다. **서명 신원·정책은 바뀌지 않습니다.** 다만 이것은 **보조 검증**(잘못된 릴리스를 일찍 거르고, 요청 전에 실패를 알리는 용도)이고, **실행을 막는 최종 게이트는 런처 하나**입니다(§3.2).
- `cosign verify-blob`(Sigstore keyless, 고정 trust root, 오프라인), 인증서 신원 `deploy-sign.yml@refs/heads/main`, issuer, `push` 트리거, ref `refs/tags/vX.Y.Z`, `branch == main`, kind별 저장소 고정
- MANIFEST 엄격 파싱, 버전은 설치본보다 높아야 함(단조 증가), `built_at` ≤ 14일, tar·멤버 sha256, 아키텍처
- 적대적 tar 처리(`unpack_bundle`)와 웹 트리 검사(`web_tree_clean`)도 재사용

달라지는 점은 검증이 끝난 결과를 **root 디렉터리에 쓰는 대신 `releases/`에 푼다**는 것과, 런처가 **복사본에서 같은 검증을 같거나 더 엄격하게 다시 한다**는 것(§3.2)입니다. 두 곳의 검증이 어긋나지 않도록 `cosign` 호출·MANIFEST 해석은 코어의 작은 라이브러리로 합치되, **라이브러리 위치는 root 사슬 안**이어야 합니다(§3.1). `evedeploy`용 사본을 따로 두지 않습니다(DevOps와 협의, §12).

### 5.2 롤백: 대상은 런처 소유의 `last_good`

rev 1은 `live/previous-gateway` 링크를 되돌리는 방식이었습니다. `evedeploy`가 이 링크를 서명된 **구버전**으로 바꿔 끼우면 floor 위의 취약한 구버전으로 되돌릴 수 있었습니다(보안 리뷰 Med). rev 2는 롤백 대상을 `evedeploy`가 쓸 수 있는 곳에서 뺍니다.

- **`last_good`**: 런처가 서명 검증을 마치고 5분 관찰(§3.2 5)을 통과시켜 승격한 릴리스입니다. 복사본(`run/last_good/`)과 기록(`state/`: 버전, MANIFEST sha256, 스키마)을 **런처가 소유**합니다. 롤백 대상은 오직 `last_good`(과 한 칸 더 `prev_good`, "직전 1개 보관"의 새 의미)입니다. 기록된 위치만 보고, `live/`·`releases/` 안의 어떤 링크나 파일도 롤백 대상을 바꾸지 못합니다. `live/previous-*`는 만들지 않습니다.
- **전환(배포)**: `evedeploy`가 `releases/`에 풀고 `live/gateway`·`live/web` 링크를 원자적으로 교체합니다(`ln -sfn` 임시 링크 후 `mv -f`). 이것은 적용 요청이고, 런처가 §3.2의 검증을 통과시킬 때만 적용됩니다.
- **자동 롤백**: ① 새 릴리스 검증 실패 → 런처가 `last_good`으로 계속 서비스. ② 적용 후 승격 전에 자식이 반복 비정상 종료 → 런처가 `last_good`으로 복귀. ③ `evedeploy`의 헬스 관찰(기존 `eve-poll observe`, 5분, 3회 연속 실패)이 실패하면 `staging/requests/`에 **롤백 요청**을 남깁니다. 런처는 이 요청을 **`last_good`으로의 복귀로만** 해석합니다(대상은 요청에 담기지 않음). 요청을 위조해도 서비스 거부가 최대입니다.
- **수동 롤백(승격 후)**: 문제 릴리스를 blocklist에 올리는 것(조이기, §5.3)과 새 높은 버전으로 되돌리는 변경(롤포워드). blocklist에 오른 릴리스는 런처가 `prev_good`·`last_good` 중 floor 이상인 것으로 대체합니다. 임의 구버전으로의 되돌림은 root 작업(코어 변경과 같은 예외)입니다.
- 스키마가 다르면 자동 롤백을 거부하는 기존 규칙(DB 다운마이그레이션 없음) 유지. 새 모델에서 `evedeploy`는 DB를 읽지 못하므로, **스키마는 서명된 MANIFEST에서 런처가 읽어 `last_good`·`prev_good` 기록에 함께 저장**하고, 현재 릴리스와 스키마가 다른 슬롯으로는 자동 롤백하지 않습니다(§12 DevOps 10 후보 해결).
- **브레이크 ACL을 믿지 않습니다**: 롤백·브레이크가 "특정 파일을 `evedeploy`가 못 지운다/못 바꾼다"는 macOS ACL·플래그 동작에 의존하지 않습니다. 보호는 소유권(런처 `_evegw`·root)과 런처의 흡수 방식(§5.3)으로만 합니다.

### 5.3 브레이크(PAUSE·blocklist·floor): 조이기 요청은 배포 계정, 판정은 런처, 풀기는 소유자

지금은 모두 root 소유라서 호출자가 못 풉니다. `evedeploy`가 쓸 수 있는 곳에 두면 침해된 `evedeploy`가 PAUSE를 지우고 floor를 낮출 수 있습니다. rev 1의 구현 후보 1·2는 `evedeploy`가 만든 파일을 못 지우게 하는 ACL·플래그에 의존했기 때문에 **폐기**하고, 후보 3을 기반으로 확정합니다.

| 동작 | 예 | 권한·방식 |
|---|---|---|
| 조이기 (더 안전한 쪽) 요청 | PAUSE, blocklist 추가, floor 올리기, 롤백 | `evedeploy`가 `staging/requests/`에 요청 파일을 남김(고정 이름·고정 형식) |
| **흡수** | 위 요청을 효력으로 | **런처가** 요청을 엄격 파싱해 **자기 소유 `state/`의 기록으로 옮기고** 효력을 냄. 이후 `evedeploy`가 요청 파일을 지워도 효력은 유지 |
| 판정 | 실제로 막기 | 런처만. `state/` 기록(floor·blocklist·PAUSE 래치)과 root 소유 floor 하한(`core/`)을 합쳐 읽음: **유효 floor = max(root 하한, `state/` 기록들)** |
| 풀기 (덜 안전한 쪽) | PAUSE 해제, blocklist 삭제, floor 내리기 | **`evedeploy` 불가.** root 소유 ack(`core/` 사슬 안)를 쓰는 소유자 작업. 일상 배포가 아니라 사고 대응이라 소유자의 root 사용을 허용(코어 변경과 같은 예외) |

- `state/`는 `_evegw` 소유라서 침해된 `_evegw`가 기록을 낮출 수는 있습니다. 하지만 floor는 **root 하한 밑으로 내려갈 수 없고**, 낮춰도 런처가 허용하는 것은 서명된 floor 이상의 릴리스뿐입니다(서명되지 않은 코드는 여전히 거부). `_evegw`는 이미 시크릿을 쥔 계정이므로 추가 위험은 작다고 봅니다(§6).
- 요청은 모두 **조이는 방향**이라, 요청을 지우거나 위조해도 얻는 것이 없습니다(침해된 `evedeploy`가 PAUSE 요청을 안 보내는 것은 막을 수 없지만, 그 경우에도 게이트는 런처입니다).
- 소유자가 풀 때의 root 사용 허용 여부는 보안 담당 확인 항목입니다(§12, 원래 질문 10).

## 6. 위협 모델 요점

| 위협 | 현재 | 새 모델 (rev 2) | 잔여 위험 |
|---|---|---|---|
| CI 서명 파이프라인 침해 | 서명된 악성 릴리스가 root로 설치(검증기는 root) | 서명된 악성 릴리스가 **`_evegw`로** 실행 (root 아님) | 서비스 계정이 가진 것(시크릿·DB)은 노출. 신원 고정·태그 보호·14일·floor·PAUSE는 그대로 |
| `evedeploy` 침해 (poller·fetch·curl 응답·tar 해석의 버그) | root 승격 경로는 sudoers 4개 동사뿐이라 영향 제한 | `releases/`·`live/`·`staging/`을 쓸 수 있으나 **게이트는 런처뿐이고 서명되지 않은 코드(gateway·웹 모두)는 거부**. 상수·런처 우회 불가, 시크릿·DB 접근 없음 | 서비스 거부(링크를 깨뜨림·요청 위조) |
| **`live/previous` 교체로 서명된 구버전 되돌림** | (해당 없음: root 상태 파일) | **롤백 대상은 런처 소유 `last_good`·`prev_good`**. `evedeploy`가 링크·`releases/`를 바꿔도 대상이 안 바뀜. 신규 릴리스는 `last_good`보다 높아야 함 | `evedeploy`가 임의 구버전을 적용시킬 수 없음. 침해된 `_evegw`가 `state/`를 낮추는 경우만 root floor 하한까지 |
| 웹 정적 파일 변조 | root 소유 `web/` | `evedeploy`가 `releases/web`을 바꿔도 **런처가 검증한 `run/web` 사본만 서빙** | 침해된 gateway가 `run/web`을 고치면 다음 전환·재기동 전까지 유지(§12 주기 재해시 후보) |
| sudoers 규칙 침해 | 있음 (4개 동사) | **규칙 자체가 없음** | 없음 |
| 코어 변조 | root 소유 | root 소유 + **상위 디렉터리까지 root 사슬, 런처가 시작 때 사슬 검사** | 변경은 사람 root 1회. 핀 sha256 + 보안 리뷰 |
| **env 주입 (`DYLD_*` 등)** | plist `EnvironmentVariables`(root 소유) | **root 소유 allow-list 밖의 키는 런처가 버림**. 보안 관련 키는 `_evegw` 소유 파일에서 읽지 않음 | allow-list 변경은 코어 변경(사람 root) |
| 검증 후 교체(TOCTOU) | root 전용 작업 디렉터리에서 검증 | 런처가 `run/` 사본을 만들고 **그 사본을 검증·해시한 뒤 실행** | `_evegw`가 침해되면 사본 변경 가능, 재기동·전환 시 복구 |
| 서비스 계정 상승 | `_evegw`는 코드 수정 불가 | `_evegw`가 `run/`을 수정 가능 (코드는 `releases/`·`last_good`에 서명본으로 남음) | 약간 약해진 부분. 런처 재검증으로 지속성 차단 |
| 시크릿 | `etc/` `_evegw` 0600 | 동일. `evedeploy` 접근 없음 | 동일 |
| 시계 오차 | `built_at` 검사 실패 → 거부 | 동일 (새 릴리스 적용 시점에만) | 안전한 방향 |
| 시스템 전반 | 배포가 root 코드 실행 경로 | **배포 경로에 root 코드 없음** | 최초 설치와 코어 변경·브레이크 풀기 때의 사람 root |

정리: **강해지는 점**은 배포 경로에서 root 코드 실행이 없어진다는 것(침해 시 최대 영향이 root → `_evegw`로 내려감)과, 롤백·브레이크 판정이 `evedeploy`가 쓸 수 있는 곳 밖에 있다는 것. **약해지는 점**은 root 전용 검증 디렉터리의 방어가 사라지고, 그 몫을 런처 재검증과 소유 경계가 대신한다는 것입니다. 이 균형은 보안 리뷰에서 **조건부로 채택**됐습니다(§13).

## 7. DevOps 초안 v2(티켓)와의 정합

초안 v2 기준(코어/페이로드 분리안은 DevOps가 취소함, 이 문서도 분리안에 기대지 않음).

- **같은 것**: 서비스 계정 권한 배포, `releases/<kind>/X.Y.Z` + 링크 원자 교체, 직전 1개 보관, 감시 wrapper 방식(R1)의 재시작, 서명 검증 순서 재사용, sudoers 제거, 최초 설치를 한 줄 명령에 포함, 처음 배포는 기존 검증된 root 경로로.
- **이 문서가 다른 것 (보안 리뷰에서 비교해 주세요)**
  1. **검증 로직 소유**: 초안 v2 기본안은 "서비스 소유(sudo 제로, 탈취 시 우회)". 여기서는 **신뢰 상수(cosign·trust root·신원)와 런처는 root 소유·1회 설치(상위 디렉터리까지 root 사슬)**, **보조 검증은 `evedeploy`, 최종 게이트는 런처(`_evegw` 실행)** 로 둡니다. 이렇게 하면 `evedeploy` 침해로 신원 상수를 바꾸거나 trust root를 교체할 수는 없습니다(바이너리·웹 교체는 서명이 없어 런처가 거부). 대가는 신뢰 상수 변경이 여전히 사람 root라는 예외 1건입니다(§2). 보안 리뷰에서 채택됐습니다.
  2. **브레이크**: 초안 v2는 서비스 소유. 여기서는 조이기 **요청**(`evedeploy`)·판정(런처가 흡수)·풀기(소유자 root)를 나눕니다(§5.3). ACL 가정은 쓰지 않습니다.
  3. **wrapper의 역할**: 초안 v2는 링크 변경 시 SIGTERM만. 여기서는 같은 wrapper(런처)가 **서명·해시 검증, 웹 사본 검증, 실패 시 `last_good`으로 계속 서비스**까지 맡습니다. 런처 코드가 커지므로 크기 제한이 필요합니다(§12 DevOps 3).
  4. **계정 분리**: 초안 v2의 B(설치 계정 ≠ 실행 계정)에 동의. 단 §6에 적었듯 `run/` 실행 사본이 `_evegw` 소유라는 점이 "서비스가 자기 코드를 못 바꾼다"를 약하게 합니다. 보안 리뷰는 이 구조를 조건(§3.1)과 함께 받아들였습니다.
- **초안 v2 질문에 대한 아키텍트 답**: (1) R1 wrapper는 launchd에서 `UserName=_evegw`, `KeepAlive` 조합으로 가능하다고 보지만 host 검증 전 가정(§12). (2) `static_dir`을 링크로 바꾸면 요청마다 디스크에서 읽으므로 호환(기존 `ui_ok` 주석 근거), 열린 파일은 링크 교체와 무관. **rev 2**: 링크는 `live/web`이 아니라 런처가 검증한 `run/web`입니다(§4, 보안 리뷰 High). (3) 환경 변수·플래그(`GW_*`, `GW_PIM_*`): rev 1의 "`etc/`의 `_evegw` 소유 env 파일로 옮기는 안"은 **폐기**했습니다(보안 리뷰 Med: 침해된 `_evegw`가 파일을 영속적으로 변조할 수 있고 `DYLD_*` 같은 주입에 악용될 수 있음). 대신 **root 소유 allow-list**를 둡니다: ① `core/`의 allow-list(root 사슬)에 있는 키만 자식에 전달하고, 값은 root 소유 plist·allow-list에 고정합니다. ② 런처는 자식 환경을 비운 뒤 allow-list 키로만 새로 만듭니다(상속 금지). ③ `DYLD_*`·`LD_*` 등 동적 로더 관련 키는 allow-list 포함 여부와 상관없이 항상 버립니다. ④ 보안 관련 키 분류(어느 키를 값까지 root가 고정하는지)는 S1에서 보안 담당과 확정합니다. 비보안 설정은 `etc/config.yaml`(기존)로 읽되 env로는 전달하지 않습니다. 변경은 코어 변경(사람 root)입니다. (4) 스키마가 다른 릴리스는 **거부 유지**(§5.2). (5) 테스트 이식은 S1에서 샌드박스 테스트를 먼저 옮겨 범위를 측정.
- **캘린더 어댑터(`/opt/eve-pim`, 사용자 `gui` 도메인 LaunchAgent, TCC)**: 이 문서 범위 밖입니다. 자동 경로에 넣지 않는다는 초안 v2·보안(#168)의 결정을 따릅니다.
- PR #168(one-shot 단계 추가)은 현 모델(sudoers 기반) 위의 변경입니다. S3에서 제거되는 것(sudoers 설치, `rehearse/unpause`의 sudo 경로)과 겹치지 않게 순서를 맞춥니다.

## 8. 단계별 이행 계획과 롤백, SemVer 판정

모든 단계는 자기 롤백 방법을 갖고, 앞 단계가 끝나 헬스·리허설이 통과한 뒤에만 다음으로 갑니다. **S2 이후는 보안 승인(S2 설치 직전 구현 리뷰) 없이 시작하지 않습니다.** 설계 리뷰(제3안 채택)는 끝났지만 구현 리뷰는 따로 필요합니다.

| 단계 | 내용 | root | SemVer | 롤백 |
|---|---|---|---|---|
| S0 | 이 설계 확정(보안 리뷰 반영 rev 2, DevOps 정합). 코드 변경 없음. 보안 리뷰: S0 문서 머지는 보안상 문제 없음 | 없음 | PATCH (문서만) | PR 되돌리기 |
| S1 | `evedeploy` 권한 검증기·전환 스크립트와 **런처의 검증 로직을 드라이런 전용**으로 추가. 현 sudo 경로와 같은 입력으로 결과를 비교하는 테스트(샌드박스, 실제 서명 픽스처 재사용). **경로 사슬 검사기**, env allow-list 파서(DYLD 등 거부), 브레이크 요청 흡수 로직도 테스트로 먼저 확정. 보안 관련 env 키 분류를 보안 담당과 확정. host 동작 변화 없음 | 없음 | MINOR (추가만) | 파일 삭제 |
| S2 | **마지막 root 설치**: `core/`(런처·cosign·trust root·env allow-list·floor 하한)와 **상위 디렉터리까지의 root 소유 사슬**, `releases/`·`live/`·`state/`·`run/` 디렉터리, 새 plist(**`UserName=_evegw`로 런처 실행**), gateway의 **`static_dir`을 `run/web`으로**, 기존 `/opt/eve-gw/bin`·`web`은 **그대로 남겨 두고** 병행 설치. 전환은 plist 교체 1회 | **1회** | **MAJOR** (보안 경계, 서비스 실행 경로, 설정 키 변경) | 이전 plist 복원 + 이전 `bin`·`web`이 남아 있어 즉시 복귀. 이 롤백은 root 필요(설치 단계의 롤백이므로 허용). **런처 설치 후 `last_good` 기록이 비어 있는 첫 기동은 "현재 서명 검증을 통과한 릴리스"를 승격 전 상태로 시작** |
| S3 | poller를 sudo 없는 경로로 전환(링크 교체 + 요청 파일 방식), **sudoers 규칙 제거**, `rehearse`(일부러 실패하는 릴리스)로 자동 롤백(`last_good` 복귀)·브레이크 흡수·소유자 풀기 경로 검증 | 규칙 삭제 1회 (또는 S2에 포함) | **MAJOR** (권한 모델 변경) | 규칙 재설치(root)로 구 경로 복귀. 구 엔트리포인트는 S4까지 남겨 둠 |
| S4 | 구 root 엔트리포인트·`uninstall` 정리, 런북·README·`eve-web/.github/workflows/release.yml` 머리말에서 `sudo` 문구 제거, `scripts/test`에서 sudo 테스트 정리. **"서명 없는 코드는 실행 안 됨" 문구는 S2의 `run/web` 전환이 검증된 뒤에만 문서에 쓴다** | 없음 | **MAJOR** (운영 인터페이스 삭제) | 이전 태그로 되돌림 |
| S5 (선택, 별도 PR) | PostgreSQL을 `_evepg`, mlx_lm을 `_evemlx` LaunchDaemon으로 분리, 백업을 `evebackup`으로 | **1회** (계정·데이터 이동) | **MAJOR** (데이터 소유 변경). **데이터 이동·삭제는 건영님 직접 승인** | `pg_dump` 백업 복원, 기존 데이터 디렉터리는 확인될 때까지 삭제하지 않음 |

버전 선: 이 변경은 **#158(6.0.0)에 묶지 않습니다.** 어느 MAJOR 번호에 싣는지는 PM이 정합니다. 배포 도구는 gateway와 별개 버전 선이라 도구만 올리는 방법도 가능하지만, 불확실하므로 MAJOR로 둡니다.

각 단계 공통 완료 조건: (1) CI 통과, (2) 보안 리뷰, (3) S2·S3·S4·S5는 건영님 직접 확인, (4) 샌드박스 테스트에 "서명 실패·변조·다운그레이드·blocklist·PAUSE 해제 시도"에 더해 rev 2 항목이 포함됨:
- `live/previous-*`·`releases/` 교체로 구버전을 적용시키려는 시도가 거부됨(롤백 대상은 `last_good`뿐)
- `releases/web` 변조 → `run/web`이 바뀌지 않음, 런처가 실패 처리하고 이전 `run/web` 유지
- 경로 사슬 구성 요소 하나라도 root 소유가 아니거나 group·other 쓰기 가능이면 런처가 기동을 거부
- 런처가 root가 아닌 `_evegw`로 실행되는지 확인(`ps`·plist 점검)
- allow-list 밖 env 키(`DYLD_INSERT_LIBRARIES` 포함)가 자식에 전달되지 않음
- `evedeploy`가 요청 파일을 지워도 PAUSE·blocklist·floor 효력이 유지됨
- 런처 검증이 배포 시점 검증보다 느슨한 항목이 없음을 비교하는 테스트(같은 픽스처로 둘의 거부 결과 대조)

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
| trust root·cosign·런처·신원 상수·**env allow-list·floor 하한** | root | `core/` (상위 디렉터리까지 root 사슬) | 읽기만 | 읽기만 | 핀 sha256. 런처는 `_evegw`로 실행 |
| 웹 정적 파일 사본 | `_evegw` | `run/web` | 접근 없음(`releases/web`에 원본 배치만) | 읽기·쓰기 | 런처가 검증 복사 |
| 롤백 슬롯·브레이크 기록 | `_evegw`(런처) | `run/last_good`·`state/` | 읽기만 | 읽기·쓰기 | 롤백 대상은 여기만(§5.2) |
| 일회용 임시 토큰 (one-shot) | 건영(owner) | 환경 변수에서만 | 없음 | 없음 | PR #168 조건 그대로 |

점검 항목(비root로 확인 가능한 것만): 각 디렉터리 `ls -ld` 소유·모드, **`core/`·plist 경로의 모든 구성 요소가 root 소유이고 group·other 쓰기 불가**(§3.1), 런처 프로세스가 `_evegw`로 실행 중, 서비스 그룹 멤버십 비어 있음, `sudo -n -l -U evedeploy` 에 NOPASSWD 없음(S3 이후).

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
3. 런처 구현 형태: bash(3.2) vs Go 단일 바이너리. 배포 코드와 같은 언어로 테스트하기 쉬운 쪽, 그리고 코어에 들어갈 코드 크기. **rev 2**: 런처가 root 사슬 검사·사본 검증·웹 사본·브레이크 흡수·env 정리를 맡게 되어 코드가 커졌습니다. bash라면 인터프리터가 읽는 파일까지 사슬 대상(§3.1)이라 Go 단일 바이너리가 유리해 보이지만 host 사정은 DevOps 판단입니다.
4. 런처 감시 방식: 링크 폴링(2초) vs 다른 방법. 롤백·전환의 재시작 지연이 허용 범위인지(기존 `kickstart -k`보다 느린지).
5. 루트 소유 `WatchPaths` 폴백(대안 D)을 준비할지. (보안은 C를 채택했으므로 C가 host에서 막힐 때만.)
6. `_evemlx`로 mlx_lm을 LaunchDaemon으로 돌릴 때 Metal(GPU) 접근이 문제없는지. 모델 오프라인 모드 가능 여부.
7. 재부팅 시 FileVault가 켜져 있으면 로그인 전에 Daemon이 올라오는지(현재 운영 방식 확인).
8. 현재 PostgreSQL(brew services)을 `_evepg`로 옮길 때의 다운타임과 데이터 이동 방법. 이 작업을 S5로 분리하는 것이 맞는지.
9. `tailscale serve`와 pf 설정(H5, R14)을 root 없이 유지할 수 있는지(예: `tailscale set --operator=` 최초 1회).
10. ~~현재 스키마 값을 `evedeploy`가 어떻게 알 것인가~~ rev 2: 서명된 MANIFEST의 스키마를 런처가 `last_good` 기록에 저장(§5.2). 이 방식으로 충분한지만 확인.
11. PR #168과 순서: #168을 먼저 머지하고 S1부터 이어가도 문제가 없는지. `setup-pim-adapter.sh`가 어떤 계정으로 무엇을 설치하는지(이 문서는 #168의 `setup-pim-adapter.sh`를 읽지 못했습니다).
12. (rev 2) `run/web` 링크 교체를 gateway가 요청마다 올바르게 따라가는지(`static_dir`이 링크를 가리킬 때 열린 파일·디렉터리 캐시 동작) 확인.
13. (rev 2) 런처가 `/Library/LaunchDaemons`의 plist와 `/opt/eve-gw/core/` 사슬 구성 요소를 `lstat`할 때, host의 기본 소유·모드(`/opt` 등)가 root 사슬 조건을 이미 만족하는지.

### 보안 담당 (rev 1 질문은 §13의 리뷰 결과로 답변됨. 남은 확인만)

1. **채택된 것의 확인(§3.1)**: "신뢰 판단 입력은 root 사슬, 검증 대상 페이로드와 요청 파일은 비신뢰 입력으로 검증 후 사용" 구분이 조건 1("런처가 읽는 모든 파일")의 의도와 맞는가. 런처가 쓰는 `state/`·`last_good`은 `_evegw` 소유라서 root 사슬이 아닙니다(런처 자신의 기록, 사용할 때 재검증).
2. 승격된 `last_good`을 재기동할 때 `built_at`을 적용하지 않는 것(§3.2 2)이 "같거나 더 엄격" 조건과 양립하는가.
3. 브레이크를 푸는 권한을 소유자의 root 사용(root 소유 ack)으로 두는 것(§5.3)이 적합한가. 소유자 계정이 침해되면 풀 수 있는 점은 그대로.
4. `run/web`·`run/gw` 사본을 침해된 `_evegw`가 고칠 수 있을 때, 주기적 재해시(예: 60초)를 런처에 넣을지 아니면 전환·재기동 때 재검증으로 충분한지.
5. 보안 관련 env 키 분류(§7 ④)와 allow-list 형식(값까지 고정할 키).
6. 승격 5분 관찰과 `evedeploy`의 롤백 요청(`last_good` 복귀로만 해석)의 서비스 거부 위험이 허용 가능한지.

## 13. 승인과 리뷰 상태

- **보안 리뷰(rev 1, 대상 head `a8fb855`): 설계 제3안 채택(조건부).** S0 문서 머지는 보안상 문제 없음. S2 이후는 구현 리뷰 후 진행. 머지·배포는 건영님이 정한 절차를 따릅니다.
- 건영님 직접 확인: S2·S3·S4·S5(모두 MAJOR). 데이터 이동·삭제와 비용·외부 노출 변경이 생기면 별도로 직접 승인.
- 이 문서의 확정 순서: DevOps 초안과 정합 → 보안 리뷰(완료, rev 2 반영) → PM 결정.

### 13.1 보안 리뷰 기록과 rev 2 반영 위치

| # | 구분 | 리뷰 내용 | rev 2 반영 |
|---|---|---|---|
| 0 | 채택 | 제3안 채택. `evedeploy`의 쓰기 범위는 `releases/`·`live/`·`staging/` 뿐이라 상수·런처 우회 불성립 | §0, §3, §4 |
| 1 | 조건 | 런처와 그가 읽는 모든 파일·plist는 상위 디렉터리까지 전부 root 소유 경로 사슬 | §3.1, §4(`core/`), §9 점검 항목, §8 S1·S2·공통 테스트 |
| 2 | 조건 | 런처는 root가 아닌 `_evegw`로 실행 | §0, §3 표·§3.1, §4(계정), §8 S2·공통 테스트 |
| 3 | 원칙 | 게이트는 런처 하나. `evedeploy`의 배포 시점 검증은 보조. 런처는 복사본에서 같거나 더 엄격하게 검증 | §0, §3.2, §5.1, §8 공통 테스트(검증 대조) |
| 4 | **High** | `static_dir`을 `live/web`에서 `run/web`(런처 검증 사본)으로 | §0 정정, §3.2, §4, §6, §8 S2 |
| 5 | Med | `live/previous` 교체는 서명된 구버전 되돌림 경로. 롤백 대상은 런처 소유 `last_good`. 브레이크 ACL 가정 불신 | §5.2, §5.3(후보 1·2 폐기), §4(`previous-*` 삭제), §6, §8 S3 |
| 6 | Med | 보안 env 키를 `_evegw` 소유 파일로 옮기는 안 비권장(영속 변조, DYLD) → root allow-list | §7(질문 답 3), §3.1, §6, §8 S1·공통 테스트 |
| 7 | 문구 | §0·§3의 "서명 없는 코드는 실행 안 됨"은 웹 공백이 닫힐 때까지 정정 | §0 "rev 1 문구 정정", §3.2, §8 S4 |

### 13.2 개정 이력

- rev 1.1: DevOps 초안 v2와 정합.
- **rev 2**: 보안 리뷰 반영(위 표). 열린 질문은 §12에 새로 정리.
