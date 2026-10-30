# 배포에서 root 제거 설계 (일상 배포는 root 없이, root는 사람의 수동 작업에만)

> 상태: **설계 제안 (rev 5, 보안 판정 10·11과 Low 반영, 미구현)** · 작성: 시스템 아키텍트 · 티켓: Notion `[OPS] 건영 sudo 제로화: 배포 도구 자가 갱신 설계` (담당 DevOps, 같은 티켓에 DevOps 초안과 아키텍트 의견 절이 따로 있음) · 관련 PR: #168(one-shot 단계 추가, DevOps)
>
> **SemVer 판정: MAJOR 예상. 보안 경계(root 소유 검증기 → 서비스 계정 권한)가 바뀌므로 보안 담당 리뷰가 필수입니다.** 불확실한 항목은 모두 MAJOR로 둡니다. MAJOR 배포는 **보안 승인과 롤백 계획이 있으면 control plane 승인으로 진행**하고, 건영님 직접 확인은 데이터 삭제·외부 공개·비용·외부 메시지에만 받습니다(rev 5, 현재 규칙, §8). 이 문서는 설계만 담고, 코드·배포·host 변경은 포함하지 않습니다.
>
> **rev 2**: 보안 담당 리뷰(대상 head `a8fb855`)에서 제3안이 채택됐고, 조건·High 1건·Med 2건·문구 정정을 반영했습니다(§13에 항목별 반영 위치).
>
> **rev 3 (아키텍트 확인, 설계 결론은 그대로)**: ① 배포 경로에 root 상주 프로세스를 두지 않는다는 것과 "풀기"의 뜻을 명시(§3.3), ② Sigstore 검증 구현 권장(§5.1.1), ③ 적용 지연의 자리(§5.4), 링크 교체 `mv -fh`(§5.2), 신뢰 사슬의 macOS 표준 링크 주의(§3.1), 열린 질문 분류(§12.1). **②·③은 보안 담당 확인이 필요합니다(§12 보안 7·8).** → rev 4에서 판정이 나와 확정했습니다.

> **rev 4 (보안 판정 반영, rev 2 리뷰 대상 `e6c59e7`)**: 보안 담당의 판정 ①~④·7~9와 Low 항목을 반영했습니다. ① `prev_good` 허용과 사용 조건(§5.2), ② `built_at` 생략은 "같거나 더 엄격"에 어긋나는 **의도된 예외**이고 조건 4개를 둠(§3.2 2), ③ 브레이크 풀기는 root, NOPASSWD 금지, ack는 래치 ID·항목별 결속(§5.3), ④ 입력은 세 분류(§3.1), 7 적용 지연은 **보조 통제**(§5.4), 8 sigstore-go 하나로 확정하고 **cosign 폴백 삭제**(§5.1.1), 만료 처리(§5.1.2), 9 소유자의 **sudo 없는 끄기**(§5.5). 제목과 §0의 "root는 최초 설치만"을 정정했습니다(root 수동 작업 목록은 §2). **보안 담당의 rev 4 head 재확인은 대기 중입니다(§13).** → rev 5에서 결과(High·Med 없음, Low 6)를 반영했습니다.

> **rev 5 (보안 리뷰 반영, rev 4 리뷰 대상 `f665cc3`)**: 보안 10(수용)·11(조건부 수용)과 Low 6건(L-r4-1~6, §13.3), 승인 문구 정정을 반영했습니다. ① `ctl/stop/`은 **비신뢰 입력**: `lstat`+`O_NOFOLLOW`+`fstat`, 일반 파일·소유 uid·정확한 이름만 허용, 내용은 읽지 않음, 항목 수 상한. stop은 **래치를 만들 뿐**이고 tools scope는 `auto_select_off` 하나로 고정, 킬스위치 파일은 단조 OR 병합, 다시 켜기는 **래치 해제(ack) → root 수정 두 단계**입니다. 한계(침해된 gateway가 `state/` 래치를 지울 수 있음)를 명시했습니다(§5.5). ② 같은 uid에서 살아남은 프로세스가 검증과 exec 사이에 `run/gw`를 바꿔칠 수 있어 §6의 "지속성 차단" 문구를 정정하고(§6), 완화(§3.4)와 실측 항목(§12 DevOps 16)을 추가했습니다. **실측 결과가 '가능'이면 런처 별도 uid `_evelaunch`가 S2의 전제**입니다(MAJOR, 보안 재리뷰). ③ Low: 시계 문구(§5.4), floor 하한 상향 절차와 풀기 도구는 root 소유 스크립트(§5.3). ④ S2~S5 승인 문구를 현재 규칙으로 바꿨습니다(§8). 판정별 반영 위치는 §13.3.

## 0. 요약

- 지금은 배포 때마다 `sudo`가 한 점 있습니다. 서명 검증·파일 교체·서비스 재시작이 모두 **root로 도는 진입점 하나**(`/usr/local/libexec/eve/deploy`)에 모여 있고, sudoers의 NOPASSWD 4개 동사(`deploy`, `deploy-web`, `rollback`, `rollback-web`)가 그 문을 엽니다.
- 목표: **일상 배포와 자동 롤백에 root를 쓰지 않습니다.** root는 최초 설치(1회)와, 사람이 수동으로 하는 **코어 변경·브레이크 풀기·다시 켜기·시크릿 회전**에만 씁니다(목록은 §2). 이후 배포·롤백은 서비스 계정(`evedeploy`)만으로 합니다. sudoers의 NOPASSWD 규칙은 없애고 새로 만들지도 않습니다(§5.3).
- 핵심 설계 결정 (rev 2~4)
  1. **root 소유는 "신뢰의 뿌리"로 한정하고 한 번만 설치**: sigstore-go를 내장한 런처, trust root, 신원 상수, env allow-list, floor 하한, launchd plist(cosign 바이너리는 두지 않음, §5.1.1). 이것들과 **그 상위 디렉터리까지 전부 root 소유 경로 사슬**이어야 합니다(§3.1). 이것이 바뀌는 일(= 코어 변경)만 사람 root입니다.
  2. **바이너리·정적 파일은 `evedeploy`가 소유**하는 `releases/` 아래 버전 디렉터리에 두고, `live/` 심볼릭 링크는 **"이 릴리스를 적용해 달라"는 신호**로만 씁니다. `live/`·`releases/` 안의 내용은 신뢰 근거가 아닙니다.
  3. **서비스는 LaunchDaemon + `UserName=_evegw`**, **런처도 root가 아닌 `_evegw`로 실행**합니다. 재시작은 sudo 없이 **런처가 링크 변경을 감지해 자식 프로세스를 교체**합니다. **실행을 막는 게이트는 런처 하나**이고, `evedeploy`의 배포 시점 검증은 보조입니다. 런처는 복사본에서 배포 시점 검증과 **같거나 더 엄격하게** 검증합니다(§3.2).
  4. **웹 정적 파일도 같은 게이트를 지납니다**: `static_dir`은 `live/web`이 아니라 **`run/web`(런처가 검증한 복사본)** 입니다(§4).
  5. **롤백 대상은 런처 소유의 `last_good`** 입니다. `live/previous-*` 링크를 바꿔 끼워도 롤백 대상이 바뀌지 않습니다(§5.2). 브레이크는 `evedeploy`가 쓸 수 있는 곳의 ACL 가정에 기대지 않습니다(§5.3).
  6. **보안 관련 env 키는 root 소유 allow-list로** 통제합니다. `_evegw` 소유 파일로 옮기는 안은 폐기했습니다(§7).
  7. (rev 3) **배포 경로에 root로 도는 코드·상주 데몬이 없습니다.** 압축 풀기는 `evedeploy`, 검증 복사는 런처(`_evegw`)가 합니다. root는 최초 설치·코어 변경·브레이크 풀기·다시 켜기·시크릿 회전에서만, 사람이 수동으로 씁니다(§2, §3.3).
  8. (rev 4) **Sigstore 검증은 런처에 내장한 sigstore-go 하나로 합니다**(보안 판정 8): 버전 핀, trust root는 오프라인 파일, **cosign 폴백 삭제**, 검증 실패는 fail-closed(§5.1.1). 만료·회전 처리는 §5.1.2.
  9. (rev 4) **입력은 세 분류**입니다: root 사슬(신뢰), `evedeploy` 쓰기 영역(비신뢰), `_evegw` 쓰기 영역 `state/`·`run/`(비신뢰, 사용할 때 재검증)(§3.1).
  10. (rev 4) **브레이크**: 조이기·끄기는 sudo 없이(요청 → 런처가 흡수, '정지'로만 해석), **풀기·다시 켜기는 root 수동**, NOPASSWD 금지, ack는 래치 ID·항목별 결속(§5.3, §5.5).
  11. (rev 4) **상태는 gateway와 web으로 나눠** 기록합니다(§4, §5.2).
  12. (rev 5) **`ctl/stop/`은 비신뢰 입력**이고 stop은 래치를 만들 뿐입니다. 킬스위치 파일은 단조 OR 병합, 다시 켜기는 ack 해제 → root 수정 두 단계(§5.5).
  13. (rev 5) **런처와 gateway가 같은 uid인 한계를 인정**하고 완화를 둡니다(§3.4). 실측 결과가 '가능'이면 런처 별도 uid `_evelaunch`가 S2의 전제입니다.
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
| 코어 파일 | 런처(sigstore-go 내장), `trusted_root.json`, 신원 상수, plist, env allow-list, floor 하한, 핀 sha256 (상위 디렉터리 포함 root 소유 사슬) | 읽기만 |
| 디렉터리 | `/opt/eve-gw` 뼈대와 소유 지정 | `releases/`, `live/`, 스테이징 안에서만 |
| 서비스 등록 | LaunchDaemon plist 설치·로드 | 없음 |
| 서명 검증 | (검증기 자체를 설치) | fetch → 검증(서명·신원·버전·해시·built_at) → 압축 풀기. **보조 검증**: 최종 게이트는 런처 |
| 전환·롤백 | - | `live/` 링크 교체(적용 요청), 헬스 관찰. **롤백 대상은 런처의 `last_good`** (`evedeploy`는 롤백 요청만) |
| 브레이크 | 초기값(floor 하한) 생성, **풀기·다시 켜기**(§5.3, §5.5) | **조이기 요청만**(§5.3). 소유자 계정은 **끄기 요청**(sudo 없음, §5.5) |
| 시크릿 | 최초 생성 | 접근 없음 |

**코어 변경은 계속 사람 root입니다**(Sigstore trust root 회전, 런처·sigstore-go 의존성 교체, 신원 상수 변경). 그래서 코어를 작게 유지하고 변경 때마다 보안 리뷰·핀·롤백 리허설을 거칩니다. 이것은 티켓 §7 결정 4("코어 변경은 사람이라는 예외를 받아들일지")의 아키텍트 답이며, **받아들이는 것을 권장합니다**.

**root를 쓰는 사람의 수동 작업 (rev 4 정정: "root는 최초 설치만"이 아니라 이 목록이 전부입니다)**

| # | 작업 | 시점 | 비고 |
|---|---|---|---|
| 1 | 최초 설치(S2) | 1회 | 계정·`core/`·plist·디렉터리 |
| 2 | 코어 변경 | 드묾 | trust root 회전·만료 전 갱신(§5.1.2), 런처·sigstore-go 의존성 교체, 신원 상수·env allow-list·floor 하한 변경 |
| 3 | **브레이크 풀기** | 사고 뒤 | PAUSE·blocklist 해제, floor 내리기(root 하한까지), 임의 구버전 되돌림, `apply_after` 앞당김. root 소유 ack를 래치 ID·항목별로 작성(§5.3, 도구는 root 소유 스크립트) |
| 4 | **다시 켜기** | 끄기 뒤 | 소유자가 건 stop 래치 해제(§5.5). 끄기는 sudo 없음, 켜기는 root |
| 5 | 시크릿 회전 | 필요할 때 | §10 (b). sudo 제로는 별도 티켓 |
| 6 | S2 설치 단계의 롤백 | 설치 문제 시 | §8 |

이 일은 모두 **비밀번호를 묻는 일반 sudo**로 합니다. **NOPASSWD 규칙은 어떤 동사에도 두지 않습니다**(기존 4개 동사는 S3에서 제거). 일상 배포·자동 롤백·조이기·끄기에는 root가 없습니다.

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

- 대상: 런처 실행 파일(과 런처가 bash 등 스크립트라면 그 인터프리터가 읽는 모든 파일), 런처 바이너리(sigstore-go 내장, 별도 `cosign` 파일 없음), `trusted_root.json`, 신원 상수, **env allow-list**(§7), floor 하한(§5.3), `/Library/LaunchDaemons`의 gateway plist.
- 조건: 경로의 **모든 구성 요소**(`/`, `/opt`, `/opt/eve-gw`, `core/`, 파일 자체, plist의 상위 `/Library`, `/Library/LaunchDaemons`)가 `root:wheel`이고 group·other 쓰기 비트가 없고, 심볼릭 링크가 아닙니다.
- macOS의 `/var`·`/etc`·`/tmp`는 `/private/...`로 가는 표준 심볼릭 링크라서 "링크가 아님" 조건을 만족하지 못합니다. 그래서 **사슬 경로(core, plist)는 이들을 거치지 않는 `/opt`·`/Library` 아래로만** 정하고, `/var`·`/etc` 아래 경로는 신뢰 사슬에 쓰지 않습니다. 사슬에 들어가는 `/opt`가 host에서 어떻게 만들어졌는지(소유·모드·합성 항목 여부)는 §12 DevOps 13에서 확인합니다.
- 런처는 시작할 때 이 사슬을 구성 요소마다 `lstat`로 **스스로 검사하고, 어긋나면 자식을 띄우지 않고 종료**합니다(fail closed). 설치 스크립트와 점검 항목(§9)에도 같은 검사를 둡니다.
- 런처는 임시 파일을 `TMPDIR` 등 공용 위치가 아니라 `run/tmp/`(`_evegw` 0700)에만 둡니다.
- **입력은 세 분류입니다 (보안 판정 ④, rev 4)**. 런처가 읽는 모든 것은 아래 중 하나이고, 신뢰 판단은 첫째에서만 읽습니다.

  | 분류 | 위치 | 신뢰 | 사용 방식 |
  |---|---|---|---|
  | ① root 사슬 | `core/`(런처·trust root·신원 상수·env allow-list·floor 하한·`ack/`), gateway plist | **신뢰** | 신뢰 판단은 여기서만 읽음 |
  | ② `evedeploy` 쓰기 영역 | `releases/`, `live/`, `staging/requests/`(런처는 `staging/dl/`·`poll/`은 읽지 않음) | **비신뢰 입력** | 엄격 파싱·검증을 통과한 것만 사용. 요청은 조이는 해석만(§5.3) |
  | ③ `_evegw` 쓰기 영역 | `state/`, `run/`(`last_good`·`prev_good` 슬롯·`run/gw`·`run/web`) | **비신뢰 입력** | 런처 자신의 기록이지만 침해된 gateway가 쓸 수 있으므로 믿지 않음. 쓸 때마다 재해시·서명 재검증, floor는 root 하한 아래로 내려가지 않게 함(§3.2 2, §5.3) |

  소유자 계정이 쓰는 끄기 요청 디렉터리(`ctl/stop/`, §5.5)는 ②와 같이 **비신뢰 입력**으로 취급하고 해석은 '정지'뿐입니다. 이 취급은 **보안 판정 10(rev 4 리뷰)으로 확정**됐습니다. 입력 검증 규칙은 §5.5입니다.
- **런처는 root가 아닌 `_evegw`로 실행**합니다(plist `UserName=_evegw`). 침해돼도 root가 아니고, 런처가 쓰는 곳은 `run/`·`state/`뿐입니다. 단 런처와 gateway가 **같은 uid**라서 생기는 한계와 완화는 §3.4에 따로 적었습니다.

### 3.2 런처의 책임 (게이트는 런처 하나)

작게, 코어에 포함, root 소유 0755, 핀 sha256. **`evedeploy`가 배포 시점에 하는 검증은 보조**이고, 이것이 통과했다는 사실은 런처가 신뢰하지 않습니다.

1. 기동·전환 때 `live/gateway`가 가리키는 릴리스 디렉터리의 내용을 먼저 **`run/`의 사본으로 복사**(TOCTOU 방지: 이후 검증과 실행은 모두 사본만. **같은 uid 프로세스와의 경합은 이것으로 막히지 않습니다**, §3.4)하고, 사본에 대해 아래를 검증합니다. **배포 시점 검증(§5.1)과 같거나 더 엄격**해야 하고, 더 느슨한 항목이 있어서는 안 됩니다.
   - 오프라인 sigstore-go 검증(런처 내장, root 사슬의 trust root·신원 상수, 네트워크·TUF 없음, §5.1.1): 인증서 신원·issuer·`push` 트리거·ref `refs/tags/vX.Y.Z`·`branch == main`·kind별 저장소 고정
   - MANIFEST 엄격 파싱, 서명된 MANIFEST의 sha256과 사본 대조(바이너리·웹 트리 모든 멤버), 아키텍처, 웹 트리 검사(`web_tree_clean`: 링크·특수 파일 금지)
   - 버전이 **floor 이상, blocklist 밖, 그리고 현재 `last_good`보다 높을 것**(단조 증가). 배포 시점에는 설치본과의 비교였는데, 런처는 자기가 승격한 `last_good`과 비교합니다.
   - 새로 적용하는(아직 승격되지 않은) 릴리스에는 `built_at` ≤ 14일(재생 방지)도 적용합니다. 배포 시점과 같은 조건입니다.
2. **이미 승격된 슬롯을 다시 띄울 때**(`last_good` 재기동·재부팅, 그리고 §5.2의 `prev_good` 사용)는 `built_at`을 적용하지 않습니다(한 달 뒤 재부팅에서 멀쩡한 서비스가 안 올라오지 않도록). 이것은 "같거나 더 엄격" 원칙(위 1)에 어긋나는 **의도된 예외**입니다(보안 판정 ②). 슬롯과 그 기록이 `_evegw` 쓰기 영역(`state/`·`run/`)에 있어서 '이미 승격됨'이라는 사실 자체를 믿을 수 없기 때문입니다. 예외는 아래 **조건을 모두** 만족할 때만 성립하고, 하나라도 어긋나면 그 슬롯을 쓰지 않습니다.
   - **기록 sha 일치**: 슬롯 사본의 MANIFEST sha256이 `state/` 기록의 값과 같고, 사본에 든 서명된 MANIFEST의 값과도 같을 것.
   - **재해시·서명 재검증**: 사본의 모든 멤버를 다시 해시하고 sigstore-go 검증을 처음부터 다시 함(§5.1.1). 기록을 믿고 건너뛰지 않음.
   - **floor·blocklist·스키마**: 유효 floor(§5.3) 이상, blocklist 밖, 서비스 중인 슬롯과 스키마 호환(§5.2).
   - **경보**: `built_at` 없이 슬롯을 쓸 때마다 로그와 경보를 남김(kind, 버전, 기록 sha, 사유).

   예외의 한계: 기록을 고친 `_evegw`가 floor 위의 오래된 **서명본**을 슬롯으로 가장할 수는 있습니다(서명되지 않은 코드는 여전히 안 됨). 그래서 floor가 상한선이고, 유효 floor는 root 하한 아래로 내려가지 않게 합니다(§5.3).
3. 검증을 통과하면 **gateway 사본과 `run/web` 사본을 만들고** 자식(`gateway`)을 **정리된 환경변수**(§7)로 실행합니다.
4. 자식이 사는 동안 `live/gateway`·`live/web` 링크 대상을 주기적으로(예: 2초) 확인합니다. 바뀌면 1부터 다시 합니다(gateway는 SIGTERM → 종료 대기 → **같은 uid 잔존 프로세스 0개 확인(§3.4)** → 교체, 웹은 `run/web` 링크를 §5.2의 `mv -fh`/`rename(2)` 방식으로 원자적으로 교체하고 재시작 없음). 새 릴리스가 검증에 실패하면 **`last_good`으로 계속 서비스**하고 실패를 로그에 남깁니다.
5. 새 릴리스를 적용한 뒤 5분 동안 자식이 비정상 종료 없이 살아 있고 `evedeploy`의 롤백 요청이 없으면 그 릴리스를 `last_good`으로 **승격**합니다(§5.2). 자식이 비정상 종료하면 런처도 비정상 종료하여 launchd `KeepAlive`와 `ThrottleInterval`을 그대로 씁니다. 승격 전에 반복 종료하면 `last_good`으로 되돌립니다.

대안 D(폴백): root 소유의 별도 LaunchDaemon이 `WatchPaths`로 `live/` 변경을 감지해 `launchctl kickstart -k system/com.llmserving.gateway`만 실행. 입력을 받지 않는 고정 명령이라 위험은 작지만 "배포 경로에 root 코드가 남는다"는 점이 목표와 어긋납니다. 런처 방식이 host에서 막힐 때만 쓰자는 의견입니다. 보안 담당은 제3안(C)을 채택했으므로 폴백은 C가 host에서 막힐 때에 한정합니다.

### 3.3 root 상주 프로세스를 두지 않는다 (용어 "풀기"의 뜻) — rev 3

- 배포 경로(받기 → 압축 풀기 → 검증 → 적용 → 롤백)에는 **root로 도는 코드가 없습니다.** 압축 풀기(`unpack_bundle`)는 `evedeploy`가 `releases/`에 하고, 런처(`_evegw`)가 `releases/`를 `run/`으로 **검증 복사**합니다. `run/`에는 `evedeploy`가 쓸 수 없습니다(`_evegw` 소유, 쓰는 것은 런처뿐).
- 이 문서와 보안 리뷰의 **"풀기는 root"는 브레이크 해제**(PAUSE·blocklist 해제, floor 내리기, §5.3)와 끄기 뒤의 **다시 켜기**(§5.5)를 뜻합니다. 압축 풀기가 아닙니다. 사고 뒤 소유자가 수동으로 하는 1회성 root 작업이며 상주 데몬이 아닙니다.
- 후보 디렉터리를 감시하는 **root LaunchDaemon(예: `unpack` 모드)은 두지 않습니다.** 이유: (a) 외부 입력(tar 멤버, 경로, 심볼릭 링크, 크기)을 root가 상시 파싱하는 새 공격면이 생기고, 이것은 R2를 없애며 제거한 것을 되살립니다. (b) "배포 경로에 root 코드 없음"(§6)과 sudoers 제거(S3)의 의미가 사라집니다. (c) 얻는 보호가 없습니다: `run/`은 이미 `evedeploy`가 못 쓰고, 낮은·같은 버전 거부는 런처의 단조 증가·`last_good` 규칙(§3.2 1)이 합니다.
- 런처가 `releases/`(비신뢰 입력)를 복사할 때는 **심볼릭 링크를 따르지 않고(`O_NOFOLLOW`·`lstat`), 일반 파일과 디렉터리만 허용하며, 파일 수·총 크기 한도**를 둡니다(웹 `web_tree_clean`과 같은 기준, §3.2 1). 한도 값은 §12 DevOps 14의 실측(크기·파일 수)으로 정합니다.
- 어떤 단계에 root 처리가 꼭 필요하다는 판단이 나오면, 이 절을 바꾸는 별도 설계 변경(MAJOR, 보안 재리뷰)으로 올립니다.

### 3.4 런처와 gateway가 같은 uid인 구조의 한계와 완화 — rev 5 (보안 판정 11, 조건부 수용)

- **한계**: 런처와 gateway 자식은 모두 `_evegw`로 돕니다(§3.1). 그래서 **같은 uid의 살아남은 프로세스**(침해된 gateway가 `setsid` 등으로 떼어낸 자식, 이전 사이클에서 남은 프로세스)가 있으면, 런처가 사본을 검증한 뒤 exec하기 **전에 `run/gw`를 바꿔칠 수 있습니다**(검증↔exec 사이의 경합). `run/`은 `_evegw` 쓰기 영역이라 소유 경계로는 막히지 않습니다. 따라서 rev 4까지의 "런처 재검증으로 지속성 차단"은 **정확하지 않은 문구**였고 §6에서 정정했습니다. 런처 재검증이 막는 것은 "다음 기동에 변조된 사본이 그대로 쓰이는 것"까지이고, 같은 uid 프로세스가 살아 있는 동안의 경합은 아래 완화로 줄입니다.
- **완화** (S1에서 구현하고 §8 공통 테스트로 확인합니다)
  1. **매 사이클 잔존 프로세스 0개 확인**: 기동·전환·슬롯 재기동으로 §3.2 1의 검증을 시작하기 전에 런처 자신을 뺀 `_evegw` uid의 프로세스를 모두 종료하고, **0개임을 다시 조회해 확인한 뒤에만** 검증을 시작합니다. 0개가 아니면(종료되지 않는 프로세스) 새 릴리스 적용을 보류하고 경보를 남기며, 서비스 중인 `last_good`은 유지합니다. 웹 사본(`run/web`)의 원자 교체는 gateway를 재시작하지 않으므로 이 확인을 적용할 수 없고, 기존 한계(§6 "웹 정적 파일 변조" 행)가 그대로입니다.
  2. **열린 fd에서 해시하고 같은 fd로 실행**: 사본을 연 뒤 그 fd에서 해시하고, 경로를 다시 열지 않고 같은 fd로 실행합니다(검증한 바이트와 실행하는 바이트를 같게). macOS에는 `fexecve`가 없어 **이 방식의 구현 가능 여부는 S1 실측 항목으로 둡니다**(보안 담당이 수용). 어떤 구현이든 아래 **필수 순서**를 지킵니다.
     1. 같은 uid 프로세스 **0개 확인**(완화 1)
     2. **검증**(해시·서명, §3.2 1)
     3. **exec 직전에 사본의 inode·크기·mtime을 다시 확인**하고 검증 때 기록한 값과 하나라도 다르면 실행하지 않고 경보(해당 사본은 버림)

     fd 실행이 구현되지 못하는 경우에도 이 순서는 필수입니다. 재확인은 경합 창을 줄일 뿐 없애지 못하므로, **실측에서 경합이 가능하다고 나오면** 완화 5(런처 별도 uid)로 갑니다.
  3. **런처에 hardened runtime과 `PT_DENY_ATTACH`**: 같은 uid의 프로세스가 런처에 디버거로 붙거나 `task_for_pid`로 접근하는 것을 줄입니다. 엔타이틀먼트 점검은 §12 DevOps 16 실측에 포함합니다.
  4. **런처↔gateway 제어 소켓을 두지 않습니다**: 같은 uid가 접속·위조할 수 있는 통로를 늘리지 않습니다. 둘 사이의 통신은 시그널과 `state/` 파일뿐이고, `run/` 안의 소켓은 런처가 읽거나 접속하지 않습니다(§4 `run/` 행).
  5. **조건부: 런처 별도 uid `_evelaunch`**: 위 실측이 '가능'이면 S2의 전제입니다(아래 판정 규칙).
- **실측과 판정 규칙**: §12 DevOps 16이 같은 uid에서 **`setsid` 생존, `ptrace`·`task_for_pid`, fd 상속**이 되는지 확인합니다(시도는 별도 승인 단계의 시험 계정에서만). 결과가 **'가능'이면, 또는 위 필수 순서를 지켜도 검증↔exec 경합이 가능하다고 나오면 런처를 별도 uid(`_evelaunch`)로 분리**하고 이를 **S2의 전제**로 올립니다(MAJOR). 이것은 §3.1·§4의 소유 경계를 바꾸는 설계 변경이므로 **MAJOR이고 보안 재리뷰**를 거칩니다. '불가'로 나오면 위 완화를 유지하고 잔여 위험으로 §6에 남깁니다. 실측 전에는 런처 uid 분리를 설계에 넣지 않습니다(§0, §3.1의 `_evegw` 실행은 이 실측 결과가 나올 때까지의 잠정안).

## 4. 디렉터리 소유·권한 모델

계정(모두 숨김, 로그인 셸 없음, 비밀번호 없음, admin 아님, 서로 그룹을 공유하지 않음):

| 계정 | 역할 | 비고 |
|---|---|---|
| root | 최초 설치와 코어 변경만 | 일상 경로에서 사용하지 않음 |
| `evedeploy` | poller·fetch·검증(보조)·전환 요청 | 이미 존재(uid 460-499). **쓰기 범위는 `releases/`·`live/`·`staging/`(그 안의 `dl/`·`requests/`) 뿐** |
| `_evegw` (451) | 런처와 gateway 실행 | 이미 존재. 런처도 이 계정(root 아님) |
| `_evepg` | PostgreSQL 실행 (신규, §9) | |
| `_evemlx` | mlx_lm 실행 (신규, §9) | |
| `evebackup` | DB 백업 | 이미 존재, 아직 미사용 |
| 건영(owner) | **끄기 요청**(sudo 없음, §5.5). 브레이크 풀기·다시 켜기·시크릿 회전(root 수동, 일반 sudo) | admin이지만 일상 배포에는 불필요 |

`/opt/eve-gw` 레이아웃 제안 (mode는 `rwx` 8진수):

| 경로 | 소유 | 모드 | 내용 · 이유 |
|---|---|---|---|
| `/opt/eve-gw` | root:wheel | 0755 | 뼈대. 하위 소유 경계를 고정 |
| `core/` | root:wheel | 0755 (파일 0755/0644) | **코어**: `launcher`(sigstore-go 내장, cosign 바이너리는 두지 않음, §5.1.1), `trusted_root.json`, 신원 상수, **env allow-list**, **floor 하한**, **`ack/`**(브레이크·stop 해제용 ack, §5.3). 상위 디렉터리 포함 root 사슬(§3.1). `evedeploy`·`_evegw` 모두 읽기만 |
| `releases/gateway/<ver>/` | evedeploy:evedeploy | 0755 (파일 0644, 실행 파일 0755) | 바이너리, `eve-gw`, `MANIFEST`, `sigstore.json`. **신뢰하지 않는 입력**: 런처가 사본을 만들어 검증한 뒤에만 사용 |
| `releases/web/<ver>/` | evedeploy:evedeploy | 0755 | 정적 UI 트리. **신뢰하지 않는 입력**(런처가 `run/web`으로 검증 복사) |
| `live/` | evedeploy:evedeploy | 0755 | 심볼릭 링크만: `gateway`, `web`. **적용 요청 신호**일 뿐 신뢰 근거가 아님. `previous-*` 링크는 **두지 않음**(롤백 대상은 `last_good`, §5.2) |
| `run/` | _evegw:_evegw | 0750 | 런처가 검증 후 복사한 실행 사본: `gw/`(gateway 사본), **`web/`(웹 정적 파일 사본, gateway의 `static_dir`)**, `last_good/{gateway,web}/`·`prev_good/{gateway,web}/`(kind별 서명 검증 완료본), `tmp/`. **런처↔gateway 제어 소켓은 두지 않음**(§3.4): `run/` 안의 소켓은 런처가 읽거나 접속하지 않음 |
| `etc/` | _evegw:_evegw | 0700 | `config.yaml`, `api_key`, `session_pepper`, 감사 HMAC 키, DB URL. 파일 0600. **`evedeploy`는 접근 불가**. 보안 관련 env 키는 두지 않음(§7) |
| `log/` | _evegw:_evegw | 0750 | 서비스 로그 |
| `state/` | _evegw:_evegw | 0755 (파일 0644, 비밀 없음) | 런처가 쓰는 상태. **gateway와 web을 나눠** `state/gateway/`·`state/web/`에 각각 둠: 현재 버전, `last_good`·`prev_good` 기록(버전·MANIFEST sha256, gateway만 스키마), PAUSE·stop 래치(고유 `latch_id` 포함), floor·blocklist 기록, `pending/`. 한 kind의 기록이 다른 kind에 영향을 주지 않음. **비신뢰 입력**(§3.1 ③). `evedeploy`는 읽기만(§5.3) |
| `/var/evedeploy/poll` | evedeploy | 0700 | poller 작업 디렉터리 (기존 그대로) |
| `/var/evedeploy/` | evedeploy:evedeploy | **0711** | 아래 `staging/`·`poll/`의 상위. 목록은 못 보고 지나가기만 가능(rev 4) |
| `staging/` | evedeploy:evedeploy | **0711** | 위치는 `/var/evedeploy/staging/`(코어 디렉터리 밖). 안은 아래 둘. 상위가 0711이라 `_evegw`는 이름을 아는 `requests/`까지만 들어갈 수 있음 |
| `staging/dl/` | evedeploy:evedeploy | 0700 | 다운로드 스테이징. 런처는 읽지 않음 |
| `staging/requests/` | evedeploy:evedeploy | **0755** (파일 0644, 비밀 없음) | 조이기·롤백 요청(§5.3). **rev 3까지는 `staging/` 전체가 0700이라 `_evegw`로 도는 런처가 요청을 못 읽었습니다.** 읽기 전용으로 열고, 쓰기는 `evedeploy`만. 런처는 `lstat`·`O_NOFOLLOW`, 일반 파일, 소유 uid가 `evedeploy`, 고정 이름, 크기 한도를 검사(비신뢰 입력, §3.1 ②) |
| `ctl/` | root:wheel | 0755 | 뼈대. 안의 `stop/`만 소유자가 씀 |
| `ctl/stop/` | 건영(owner) | 0755 (파일 0644) | 소유자의 **끄기 요청** 파일(§5.5). sudo 없이 소유자 계정이 씀. 런처는 읽기만 하고, 항목마다 `lstat`·`O_NOFOLLOW`·`fstat`로 검사하며 **내용은 읽지 않음**(비신뢰 입력, §5.5) |

핵심 결과:
- **`evedeploy` 소유 영역은 `releases/`, `live/`, `staging/`(`dl/`·`requests/`), `poll/` 뿐**이고, 거기에 있는 코드는 서비스가 쓰기 전에 **런처가 서명으로 다시 확인**합니다. 상수·런처를 바꾸거나 우회하는 경로는 이 범위에 없습니다.
- **코드가 `_evegw` 소유가 아닙니다.** 서비스가 자기 코드를 못 고친다는 4.0.0 규칙(runbook §0)의 의도를 "root 소유" 대신 "배포 계정 소유 + 실행 시점 서명 검증"으로 지킵니다. 실행 사본(`run/`)은 `_evegw` 소유라서 침해된 gateway가 사본을 고칠 수는 있지만, 다음 기동·전환 때 런처가 `releases/`의 서명본(또는 `last_good`)으로 다시 만들고 다시 검증합니다.
- **웹 정적 파일은 `run/web`에서 서빙합니다** (보안 리뷰 High). rev 1은 `static_dir=live/web`이라 `evedeploy`가 쓴 파일을 gateway가 검증 없이 읽었습니다. 이제 `static_dir`은 `run/web`이고, 런처가 `releases/web/<ver>`를 사본으로 만들어 검증(서명·해시·`web_tree_clean`)한 뒤 `run/web` 링크를 원자적으로 교체합니다. gateway는 `static_dir`을 요청마다 디스크에서 읽으므로(`ui_ok` 주석) **UI 배포는 재시작이 필요 없습니다**. 이 설정 변경은 gateway 설정(`config.yaml`) 변경이라 S2에 들어갑니다.
- 그룹 규칙: **`evedeploy`를 `_evegw`·`_evepg`·`_evemlx` 그룹에 넣지 않습니다.** `requests/`는 그룹이 아니라 other 읽기(0755)로 열어 둡니다(그룹 공유 금지 원칙 유지). 4.0.0 runbook의 "그룹 멤버십이 비어 있어야 함" 점검(`dscl . -read /Groups/_evegw GroupMembership`)을 모든 서비스 그룹으로 늘립니다.

## 5. 서명 검증과 롤백 유지

### 5.1 검증은 그대로, 실행 위치와 최종 게이트만 바꿉니다

기존 `eve-deploy`가 하던 검증을 같은 순서로 `evedeploy` 권한 스크립트가 합니다. **서명 신원·정책은 바뀌지 않습니다.** 다만 이것은 **보조 검증**(잘못된 릴리스를 일찍 거르고, 요청 전에 실패를 알리는 용도)이고, **실행을 막는 최종 게이트는 런처 하나**입니다(§3.2).
- Sigstore 검증(keyless, 고정 trust root, 오프라인. 지금 `eve-deploy`는 cosign을 호출하고, 새 구조는 같은 주장을 sigstore-go로 검증함, §5.1.1), 인증서 신원 `deploy-sign.yml@refs/heads/main`, issuer, `push` 트리거, ref `refs/tags/vX.Y.Z`, `branch == main`, kind별 저장소 고정
- MANIFEST 엄격 파싱, 버전은 설치본보다 높아야 함(단조 증가), `built_at` ≤ 14일, tar·멤버 sha256, 아키텍처
- 적대적 tar 처리(`unpack_bundle`)와 웹 트리 검사(`web_tree_clean`)도 재사용

달라지는 점은 검증이 끝난 결과를 **root 디렉터리에 쓰는 대신 `releases/`에 푼다**는 것과, 런처가 **복사본에서 같은 검증을 같거나 더 엄격하게 다시 한다**는 것(§3.2)입니다. 두 곳의 검증이 어긋나지 않도록 서명 검증·MANIFEST 해석은 런처의 `verify` 서브커맨드(§5.1.1) 하나로 합치되, **라이브러리 위치는 root 사슬 안**이어야 합니다(§3.1). `evedeploy`용 사본을 따로 두지 않습니다(DevOps와 협의, §12).

### 5.1.1 Sigstore 검증 구현: 런처에 내장한 sigstore-go 하나 (cosign 폴백 없음) — rev 4 (보안 판정 8)

- **결정**: 런처(Go 단일 바이너리)가 sigstore-go로 직접 검증합니다. `evedeploy`의 보조 검증도 같은 런처의 읽기 전용 `verify` 서브커맨드를 호출해 **구현이 한 벌**입니다(§5.1).
- **버전 핀**: sigstore-go와 모든 전이 의존성을 `go.mod`·`go.sum`·vendor로 고정하고 런처 sha256을 핀합니다. 의존성 갱신은 **코어 변경**(root 수동, 보안 리뷰, 아래 대조 테스트 재실행)입니다.
- **trust root는 오프라인**: 핀된 `trusted_root.json`을 `core/`에서 파일로 읽습니다. TUF 갱신·네트워크·외부 조회가 없습니다. 검증 중 네트워크를 쓰지 않는지는 네트워크를 막은 상태의 테스트로 확인합니다.
- **주장**: 현재 cosign이 거는 것과 같은 강도(issuer, 인증서 신원 `deploy-sign.yml@refs/heads/main`, `push` 트리거, ref `refs/tags/vX.Y.Z`, `branch == main`, kind별 저장소 고정)를 걸고, 번들의 인증서 체인·투명성 로그 증거·서명을 검증합니다.
- **fail-closed**: 어떤 오류든(번들 없음, 파싱 실패, trust root 읽기 실패·손상·만료, 주장 불일치, 시간 초과, panic) **거부**입니다. "검증하지 못함"을 "통과"로 읽는 경로가 없습니다. 신규 릴리스는 적용하지 않고 `last_good`으로 서비스하며, 슬롯 재기동에서 검증이 실패하면 그 슬롯을 쓰지 않습니다(§3.2 2, §5.2). 모든 슬롯이 실패하면 코드를 검증 없이 띄우지 않고 서비스를 올리지 않은 채 경보합니다(서비스 거부가 미검증 코드 실행보다 낫습니다).
- **cosign 폴백은 뺍니다**: 폴백이 있으면 두 검증기 중 느슨한 쪽이 실제 게이트(OR)가 되고 하위 프로세스·환경·HOME 격리 코드가 필요해집니다. sigstore-go에서 같은 강도로 걸 수 없는 주장이 나오면 폴백으로 넘기지 않고 **설계 변경(MAJOR, 보안 재리뷰)** 으로 올립니다. 두 검증기를 함께 두는 안을 다시 검토하더라도 **같은 정책으로 둘 다 통과해야 실행**(AND)이고 OR는 금지입니다.
- **코어에 cosign이 없습니다.** S2 설치 산출물은 런처·`trusted_root.json`·상수입니다. 지금 `/usr/local/libexec/eve/cosign`은 현 sudo 경로용이고 S4에서 정리합니다.
- **대조 테스트(S1)**: cosign은 **테스트 전용 기준**으로만 씁니다(런타임·코어에 없음). 같은 픽스처(정상, MANIFEST 변조, 다른 신원·ref·sha·trigger, 만료 인증서, 번들 없음, trust root 누락·손상)에서 기준과 런처의 판정이 같아야 하고, 거부 항목은 모두 거부여야 합니다.
- **의존성 승인**은 보안 판정 8로 받았습니다(핀·오프라인·폴백 삭제·fail-closed가 조건).

### 5.1.2 cosign·trust root 만료 처리 — rev 4 (보안 Low)

- **무엇이 만료되나**: ① `trusted_root.json`에 든 인증 기관(Fulcio)과 투명성 로그(Rekor) 키의 **유효 기간**. ② Fulcio 인증서는 수 분짜리라서, 검증은 인증서 자체의 만료가 아니라 **번들에 든 투명성 로그 통합 시각이 인증서·키 유효 기간 안인지**를 봅니다. 그래서 오래된 릴리스도, trust root가 그 시각의 키를 **계속 담고 있는 한** 재검증됩니다. ③ 핀된 의존성(sigstore-go)과 테스트 전용 cosign은 만료되지 않지만 낡습니다(보안 공지가 나오면 코어 변경).
- **회전 규칙**: trust root 회전은 코어 변경(root 수동)입니다. **구 키·구 기간 항목은 지우지 않고 추가만** 합니다. 지우면 `last_good`·`prev_good`이 재검증에 실패해 기동하지 못합니다.
- **임박 경보**: 런처가 기동 때와 하루 1회 trust root 안의 **가장 늦은 유효 종료 시각**을 읽어(하드코딩하지 않음) 남은 기간이 60일 이하면 경고, 30일 이하면 심각 경보를 남깁니다.
- **만료 뒤**: 새 릴리스 검증은 실패하므로 **적용을 거부**합니다(fail-closed, 새 배포만 멈춤). `last_good` 재기동은 구 항목이 남아 있으면 통과해 서비스가 유지됩니다. 재검증이 실패하는 슬롯은 쓰지 않습니다(§5.1.1). 호스트 시계가 **빨리 가는** 경우에만 만료 판정이 거부 쪽(안전)으로 틀립니다. 시계가 느리거나 뒤로 가면 만료 판정이 늦어집니다(§5.4 시계 오차).
- **회전 리허설**: 새 trust root를 설치하기 전에 샌드박스에서 ① `last_good`·`prev_good` 재검증 통과, ② 신규 릴리스 검증 통과, ③ 구 항목을 일부러 뺀 경우 재검증이 실패함을 확인합니다.
- 실측: 지금 설치된 `trusted_root.json`의 항목별 유효 종료 시각과 설치된 cosign의 핀 버전(읽기 전용, §12 DevOps 17).

### 5.2 롤백: 대상은 런처 소유의 `last_good`

rev 1은 `live/previous-gateway` 링크를 되돌리는 방식이었습니다. `evedeploy`가 이 링크를 서명된 **구버전**으로 바꿔 끼우면 floor 위의 취약한 구버전으로 되돌릴 수 있었습니다(보안 리뷰 Med). rev 2는 롤백 대상을 `evedeploy`가 쓸 수 있는 곳에서 뺍니다.

- **`last_good`**: 런처가 서명 검증을 마치고 5분 관찰(§3.2 5)을 통과시켜 승격한 릴리스입니다. 복사본(`run/last_good/`)과 기록(`state/`: 버전, MANIFEST sha256, 스키마)을 **런처가 소유**합니다. 롤백 대상은 오직 `last_good`(과 한 칸 더 `prev_good`, "직전 1개 보관"의 새 의미)입니다. **슬롯과 기록은 kind별**(`gateway`, `web`)로 따로 있고(§4 `state/gateway/`·`state/web/`) 한 kind의 롤백이 다른 kind를 건드리지 않습니다. 기록된 위치만 보고, `live/`·`releases/` 안의 어떤 링크나 파일도 롤백 대상을 바꾸지 못합니다. `live/previous-*`는 만들지 않습니다.
- **`prev_good` 사용은 허용합니다 (보안 판정 ①, rev 4)**. 쓸 수 있는 때: `last_good`이 blocklist·floor에 걸렸거나 반복 기동에 실패할 때. **쓸 때는 §3.2 2의 조건을 전부** 적용합니다: 기록 sha 일치, **재해시와 서명 재검증**, **floor·blocklist·스키마 확인**, **경보**. 하나라도 어긋나면 쓰지 않습니다. 새 릴리스를 승격하면 기존 `last_good`이 `prev_good`이 되고(그 사이 슬롯은 재검증 없이 옮기지 않고 사용할 때마다 위 검증을 거침), 더 오래된 슬롯은 없습니다.
- **전환(배포)**: `evedeploy`가 `releases/`에 풀고 `live/gateway`·`live/web` 링크를 원자적으로 교체합니다(임시 링크를 만든 뒤 **`mv -fh`**, 또는 Go의 `rename(2)`. 대상이 디렉터리를 가리키는 링크일 때 `mv -f`는 링크를 따라 들어가 그 디렉터리 안으로 임시 링크를 옮기는 함정이 있어 `-h`가 필요합니다. 테스트로 확인: 교체 뒤 이전 릴리스 디렉터리에 임시 링크가 생기지 않음). 이것은 적용 요청이고, 런처가 §3.2의 검증을 통과시킬 때만 적용됩니다. **링크를 바꾸는 모든 곳(`live/gateway`·`live/web`, 런처의 `run/web`, 슬롯 승격)은 이 방식 하나로 통일합니다.** rev 4 점검 결과: 이 문서의 링크 교체 서술은 위 한 곳과 이를 가리키는 §3.2 4·§4뿐이고 단독 `mv -f`·`ln -sf`는 없습니다. 레포의 현재 스크립트(`eve-deploy` 등)는 링크가 아니라 파일·디렉터리를 옮기므로 이 함정에 해당하지 않고, 링크 교체 코드는 S1에서 새로 생깁니다. 구현 PR에서는 `mv -f`·`ln -sf`가 링크 대상에 쓰이지 않는지 grep으로 점검하는 항목을 둡니다(§8).
- **자동 롤백**: ① 새 릴리스 검증 실패 → 런처가 `last_good`으로 계속 서비스. ② 적용 후 승격 전에 자식이 반복 비정상 종료 → 런처가 `last_good`으로 복귀. ③ `evedeploy`의 헬스 관찰(기존 `eve-poll observe`, 5분, 3회 연속 실패)이 실패하면 `staging/requests/`에 **롤백 요청**을 남깁니다. 런처는 이 요청을 **`last_good`으로의 복귀로만** 해석합니다(대상은 요청에 담기지 않음). 요청을 위조해도 서비스 거부가 최대입니다.
- **수동 롤백(승격 후)**: 문제 릴리스를 blocklist에 올리는 것(조이기, §5.3)과 새 높은 버전으로 되돌리는 변경(롤포워드). blocklist에 오른 릴리스는 런처가 `prev_good`·`last_good` 중 floor 이상인 것으로 대체합니다. 임의 구버전으로의 되돌림은 root 작업(코어 변경과 같은 예외)입니다.
- 스키마가 다르면 자동 롤백을 거부하는 기존 규칙(DB 다운마이그레이션 없음) 유지. 새 모델에서 `evedeploy`는 DB를 읽지 못하므로, **스키마(gateway 슬롯에만 있고 web은 해당 없음)는 서명된 MANIFEST에서 런처가 읽어 `last_good`·`prev_good` 기록에 함께 저장**하고, 현재 릴리스와 스키마가 다른 슬롯으로는 자동 롤백하지 않습니다(§12 DevOps 10 후보 해결).
- **브레이크 ACL을 믿지 않습니다**: 롤백·브레이크가 "특정 파일을 `evedeploy`가 못 지운다/못 바꾼다"는 macOS ACL·플래그 동작에 의존하지 않습니다. 보호는 소유권(런처 `_evegw`·root)과 런처의 흡수 방식(§5.3)으로만 합니다.

### 5.3 브레이크(PAUSE·blocklist·floor): 조이기 요청은 배포 계정, 판정은 런처, 풀기·다시 켜기는 소유자의 root

지금은 모두 root 소유라서 호출자가 못 풉니다. `evedeploy`가 쓸 수 있는 곳에 두면 침해된 `evedeploy`가 PAUSE를 지우고 floor를 낮출 수 있습니다. rev 1의 구현 후보 1·2는 `evedeploy`가 만든 파일을 못 지우게 하는 ACL·플래그에 의존했기 때문에 **폐기**하고, 후보 3을 기반으로 확정합니다.

| 동작 | 예 | 권한·방식 |
|---|---|---|
| 조이기 (더 안전한 쪽) 요청 | PAUSE, blocklist 추가, floor 올리기, 롤백 | `evedeploy`가 `staging/requests/`에 요청 파일을 남김(고정 이름·고정 형식) |
| **흡수** | 위 요청을 효력으로 | **런처가** 요청을 엄격 파싱해 **자기 소유 `state/`의 기록으로 옮기고** 효력을 냄. 이후 `evedeploy`가 요청 파일을 지워도 효력은 유지 |
| 판정 | 실제로 막기 | 런처만. `state/` 기록(floor·blocklist·PAUSE 래치)과 root 소유 floor 하한(`core/`)을 합쳐 읽음: **유효 floor = max(root 하한, `state/` 기록들)** |
| 풀기·다시 켜기 (덜 안전한 쪽) | PAUSE 해제, blocklist 삭제, floor 내리기, stop 래치 해제(§5.5) | **`evedeploy`·`_evegw` 불가.** **root 수동 작업**(일반 sudo, **NOPASSWD 금지**). root 소유 ack(`core/ack/`)를 **래치 ID와 항목별로 묶어** 씀. 일상 배포가 아니라 사고 대응이라 소유자의 root 사용을 허용(보안 판정 ③) |

- `state/`는 `_evegw` 소유라서 침해된 `_evegw`가 기록을 낮출 수는 있습니다. 하지만 floor는 **root 하한 밑으로 내려갈 수 없고**, 낮춰도 런처가 허용하는 것은 서명된 floor 이상의 릴리스뿐입니다(서명되지 않은 코드는 여전히 거부). `_evegw`는 이미 시크릿을 쥔 계정이므로 추가 위험은 작다고 봅니다(§6).
- 요청은 모두 **조이는 방향**이라, 요청을 지우거나 위조해도 얻는 것이 없습니다(침해된 `evedeploy`가 PAUSE 요청을 안 보내는 것은 막을 수 없지만, 그 경우에도 게이트는 런처입니다).
- 소유자가 풀 때의 root 사용은 **보안 판정 ③에서 적합**으로 확정됐습니다. 소유자 계정이 침해되면 풀 수 있는 점은 그대로입니다.
- **ack 결속 규칙 (보안 판정 ③, rev 4)**
  - 런처가 래치(PAUSE·blocklist 항목·floor 상승·stop)를 만들 때 **고유 `latch_id`**(kind 포함, 무작위 128비트)를 `state/<kind>/`에 기록하고 로그·경보에 출력합니다.
  - ack **한 장은 래치 한 개와 항목 한 개**만 풉니다: 내용은 `latch_id`, 항목(PAUSE, blocklist의 특정 버전 하나, floor의 새 값 하나, stop scope 하나), 작성 시각입니다.
  - 런처는 `latch_id`·항목이 현재 래치와 **정확히 일치할 때만** 그 항목 하나를 풉니다. 다른 항목, 다른 래치, 나중에 생기는 같은 종류의 래치는 풀지 않습니다. 와일드카드나 "전체 해제" ack는 없습니다. 래치마다 새 ID가 생기므로 **옛 ack를 다시 써도 무효**입니다.
  - ack는 `core/ack/`(root 사슬)에만 놓이고 `evedeploy`·`_evegw`는 만들거나 고칠 수 없습니다. 사용된 ack는 런처가 `state/`에 소비 기록과 로그를 남깁니다.
  - **NOPASSWD 금지**: ack 작성·브레이크 해제 동사를 sudoers에 NOPASSWD로 두지 않습니다. 점검은 `sudo -n -l`에 NOPASSWD 항목이 없음으로 합니다(S3 이후 전체).
  - floor는 root 하한(`core/`) 밑으로 내릴 수 없습니다. 하한 변경은 코어 변경입니다.

- **`state/` 래치는 지워질 수 있습니다 (L-r4-6, L-r4-3)**: PAUSE·stop·blocklist 래치와 기록은 `_evegw` 쓰기 영역(`state/`)에 있어서 **침해된 gateway가 지울 수 있습니다**(root ack 없이 사라짐). 그래서 **PAUSE·stop·blocklist는 운영 통제이고 보안 경계가 아닙니다.** 문서·테스트·보고에서 이 래치가 침해된 gateway를 막는다고 쓰지 않습니다. 침해돼도 유지되는 것은 root 사슬의 하한(floor 하한·신원 상수·trust root)과 서명 검증뿐입니다. 이 한계는 §5.5·§6에도 같은 말로 적었습니다.
- **풀기 도구는 root 소유 스크립트 (보안 Low, rev 5)**: ack 작성·브레이크 풀기·다시 켜기를 돕는 도구는 `core/`(root 사슬, §3.1) 안의 **root 소유 스크립트**(`root:wheel` 0755, group·other 쓰기 불가)로 둡니다. 소유자가 비밀번호를 묻는 일반 sudo로 이 스크립트를 실행합니다. 소유자·`evedeploy`·`_evegw`가 쓸 수 있는 경로의 스크립트·바이너리는 sudo로 실행하지 않습니다(쓰기 가능한 파일을 root로 실행하는 경로를 막기 위해서입니다). 도구는 입력(래치 ID·항목)을 위 ack 형식으로만 받고 와일드카드를 거부합니다. **이 도구를 바꾸는 것은 코어 변경과 같습니다**(root 수동, 핀 sha256 갱신, 보안 리뷰, §2·§3.1). 일상 배포 경로로는 바꿀 수 없습니다(L-r4-5).
- **floor 하한 상향 절차 (보안 Low, L-r4-5, rev 5)**: 하한은 `core/`(root 사슬)에 있어 올리는 것도 코어 변경(root 수동)입니다. **올리는 것만** 이 절차로 하고, 내리는 것은 별도 코어 변경(MAJOR, 보안 재리뷰)이며 풀기 도구로 하지 않습니다.
  1. 새 값과 사유를 보안 담당이 확인합니다.
  2. **새 값 이상인 릴리스를 먼저 배포해 `last_good`으로 승격**합니다. 순서를 어기면 상향하는 순간 `last_good`이 floor 미달이 되어 서비스를 올리지 못합니다(§5.1.1의 "모든 슬롯 실패" 경보 경로). `prev_good`이 새 값 미만이면 그 슬롯은 쓸 수 없게 됩니다(§5.2).
  3. root가 하한 파일을 임시 파일 작성 후 `rename(2)`로 교체합니다(사슬 조건 유지: `root:wheel`, group·other 쓰기 불가, 심볼릭 링크 아님).
  4. 런처가 새 하한을 반영하는 시점(재기동 또는 폴링)은 S1에서 확정하고, 반영 뒤 로그에 새 유효 floor가 찍히는지 확인합니다.

### 5.4 적용 지연: 보조 통제이고 필수 게이트가 아닙니다 (rev 4, 보안 판정 7)

티켓(DevOps 구현 계획 §G)에 기록된 보안 결정을 이 설계의 자리에 놓습니다: 새 릴리스는 **24시간 뒤 자동 적용(최소 12시간), 적용 직전 재검증, 건영님 개입 없음**. 보안 담당은 이를 수용하면서 **보조 통제로 문서화**하라고 판정했습니다(판정 7).

- **필수 게이트가 아닌 이유**: 최초 발견 시각(`first_seen`)이 `_evegw` 소유 `state/`에 있어서 **gateway가 침해되면 기록을 과거로 소급해 지연을 건너뛰게 할 수 있습니다.** 하한(12h)은 root 사슬에 있어도 `first_seen` 자체는 아닙니다.
- **실제로 막는 것**은 런처의 서명 검증·floor·blocklist·PAUSE 게이트(§3.2 1)뿐입니다. 지연은 "서명된 정상 CI 산출물이 사고 때(예: CI 침해로 서명된 악성 릴리스)" 사람이 PAUSE·blocklist를 걸 시간을 버는 **보조 통제**입니다.
- 문서·테스트·보고에서 지연을 보안 경계로 쓰지 않고, "지연 덕에 안전하다"는 주장을 하지 않습니다. 선택 완화: `first_seen`을 추가 전용 감사 로그(`log/`)에도 남겨 사후 대조.

- **시계 기준**: CI가 쓴 `built_at`이 아니라 **런처가 후보를 처음 본 시각**. 런처가 `live/` 링크 대상이 `last_good`과 다른 새 릴리스를 처음 발견하면 `state/pending/<kind>-<ver>`에 `first_seen`을 기록하고 `apply_after = first_seen + max(24h, 하한)`으로 계산합니다. **하한(12h)은 `core/`(root 사슬)** 에 두어 `_evegw`가 낮추지 못합니다(단 위 "필수 게이트가 아닌 이유": `first_seen` 기록은 소급될 수 있음). **시계 오차 (보안 Low, L-r4-4, rev 5)**: 호스트 시계는 **빨리 가는 경우만 안전한 방향**입니다. `built_at` 14일 검사와 trust root 만료 판정(§5.1.2)은 시계가 빠르면 거부 쪽으로 틀립니다. 시계가 느리거나 뒤로 가면 오래된 릴리스가 14일 안으로 보이고 만료 판정이 늦어집니다. `apply_after`도 시계가 앞으로 뛰면 지연이 줄어듭니다. **호스트의 시간 동기(NTP) 상태**는 실측 항목에 넣습니다(§12 DevOps 20). 그래서 문서·테스트·보고에서 "시계 오차는 안전한 방향"이라고 쓰지 않고, 시계에 기대는 검사(`built_at`·만료·지연)는 **보조 통제**로 봅니다. 경계는 서명·floor·blocklist·PAUSE입니다.
- **지연 중**: 런처는 `last_good`으로 서비스를 유지하고, **재부팅·재기동 때도 `last_good`**을 띄웁니다(링크가 가리키는 후보를 지연 전에 실행하지 않음). §3.2 1의 "기동·전환 때 `live/gateway`가 가리키는 릴리스를 복사"는 "지연이 끝난 후보만"으로 읽습니다.
- **적용 직전**: `apply_after`가 지난 뒤 사본에서 §3.2 1의 검증을 **전부 다시** 합니다(서명·해시·floor·blocklist·PAUSE, `built_at` 14일은 첫 적용이므로 포함). 24시간 동안 `releases/`가 바뀐 경우를 잡습니다. 이 사이 PAUSE가 걸리면 적용하지 않고, blocklist에 오른 후보는 pending에서 지웁니다.
- **롤백은 지연하지 않습니다**(`last_good` 복귀, §5.2). 지연을 건너뛰는 긴급 경로는 두지 않는 것이 기본이며(개입 없음 원칙), 필요하면 소유자 root 작업(브레이크 해제와 같은 예외)으로만 `apply_after`를 앞당깁니다.
- **`evedeploy` 쪽 영향**: 링크를 바꿔도 적용되지 않으므로 poller의 5분 헬스 관찰(§5.2 ③)은 **`state/`에서 적용(`TRIAL` 시작)이 확인된 뒤에** 시작해야 합니다. 안 그러면 지연 중 "새 버전이 안 뜬다"를 실패로 보고 롤백·PAUSE를 겁니다.
- 지연은 gateway와 web 둘 다에 적용하는 것을 기본으로 합니다. web만 짧게 하는 등 kind별 차등은 보안 7에서 정합니다.

### 5.5 소유자의 끄기(stop) 요청: sudo 없이 허용, 다시 켜기는 root — rev 4 (보안 판정 9), rev 5 (보안 판정 10 수용)

긴급 정지(툴 자동 선택 킬스위치 `tools-killswitch.yaml`의 `auto_select_off`, #158, 와 배포 PAUSE)가 5초 안에 sudo 없이 되어야 한다는 요구입니다. 보안 담당은 **끄기(조이기)는 sudo 없이 허용, 다시 켜기는 root 유지**로 판정했습니다(판정 9). 입력 취급과 구현 방식은 **판정 10으로 수용**됐습니다.

- **요청**: 소유자 계정이 `ctl/stop/`에 고정 이름의 파일(`stop-<scope>`)을 만듭니다. scope는 **고정 목록**입니다: `tools`, `deploy-gateway`, `deploy-web`. **파일 내용은 읽지 않습니다**(존재만 봄). rev 4의 "고정 형식 한 줄만 허용" 안은 폐기합니다. **scope 매핑은 아래 표로 고정**하며 표에 없는 대응은 없습니다(L-r4-2).

  | 파일 | 효과 | 바뀌는 것 |
  |---|---|---|
  | `stop-tools` | `tools` 래치 → 킬스위치 켜기 | **`auto_select_off` 하나만**(다른 키 불변) |
  | `stop-deploy-gateway` | gateway kind의 PAUSE 래치 | `state/gateway/`의 PAUSE 래치 |
  | `stop-deploy-web` | web kind의 PAUSE 래치 | `state/web/`의 PAUSE 래치 |
- **입력 검증 (판정 10, L-r4-1)**: `ctl/stop/`은 소유자 계정이 쓰는 곳이라 §3.1 ②와 같은 **비신뢰 입력**입니다. 런처는 먼저 **디렉터리 자체를 검사**하고(`ctl/`·`ctl/stop/`을 `lstat`: 심볼릭 링크가 아니고, 소유 uid가 소유자 계정(`ctl/`은 root), group·other 쓰기 비트 없음, 모드가 0755 이하) 하나라도 어긋나면 **요청 없음으로 처리하고 경보**를 남깁니다. 통과하면 디렉터리를 나열하고, 항목마다 아래를 **모두** 통과한 것만 '요청 있음'으로 셉니다.
  1. `lstat`으로 종류를 먼저 확인하고 `O_NOFOLLOW`로 연 뒤 `fstat`으로 다시 확인합니다(확인과 열기 사이 바꿔치기 방지). 심볼릭 링크·디렉터리·특수 파일은 거부합니다.
  2. **일반 파일**일 것.
  3. **소유 uid가 소유자 계정**일 것(소유자 uid는 root 사슬 `core/`의 상수).
  4. **이름이 `stop-(tools|deploy-gateway|deploy-web)`와 정확히 일치**할 것(전체 일치. 접두·접미·대소문자 변형 불가).
  5. **내용을 읽지 않음**(크기·내용을 해석에 쓰지 않음).
  6. **읽을 수 없는 항목**(열기·`fstat` 실패, 권한 거부, 디렉터리 읽기 오류 포함)은 **'요청 없음'으로 처리**합니다. 오류를 요청으로 읽거나 재시도 루프로 키우지 않고 경보 한 줄만 남깁니다.

  **항목 수 상한 64**: 한 번에 보는 디렉터리 항목 수는 64개까지입니다. 넘으면 64개까지만 보고 경보를 남깁니다. 검증에서 떨어진 항목과 목록 밖 이름은 무시하고 로그 한 줄만 남깁니다.
- **해석**: 런처가 주기적으로(목표 1초 이하) 검증을 통과한 요청을 **'정지' 한 가지로만** 해석합니다. **stop은 래치를 만들기만 합니다**: 런처가 `state/<kind>/`에 래치(고유 `latch_id`, §5.3)를 기록하는 것이 전부이고, 값을 읽어 쓰거나, 다른 키를 바꾸거나, 풀기·켜기 쪽으로 쓰는 경로가 없습니다.
  - `deploy-gateway`·`deploy-web`: 해당 kind의 PAUSE 래치.
  - `tools`(`stop-tools`): **scope는 킬스위치 파일의 `auto_select_off` 하나로 고정**합니다(위 표, L-r4-2). 다른 키는 바꾸지 않습니다.
- **킬스위치 파일 갱신 (판정 10)**: `tools` 래치가 있으면 런처가 킬스위치 파일의 `auto_select_off`를 켠 상태로 맞춥니다. 구현은 **런처가 파일을 직접 갱신하는 방식**으로 정하고, gateway가 `state/` 래치를 직접 읽는 방식은 쓰지 않습니다. 파일 위치·형식은 #158에 맞춰 S1에서 확정하고 아래 규칙은 바꾸지 않습니다.
  - **단조 OR 병합**: 새 값 = 기존 값 OR 래치. 이미 켜진 값을 내리는 경로가 없습니다.
  - 쓰기는 **같은 디렉터리에 임시 파일을 쓰고 `rename(2)`로 교체**합니다. 파일은 `_evegw` 소유 **0600**입니다.
  - **깨진 파일**(파싱 실패·형식 위반·예상 밖 키)은 런처가 고치거나 덮어쓰지 않고 **경보만** 남깁니다. 래치는 유지됩니다.
- **흡수**: §5.3과 같이 런처가 요청을 `state/<kind>/`의 래치로 옮깁니다. 요청 파일을 지워도 래치는 유지됩니다. scope당 래치는 하나이고 중복 요청은 무시합니다(로그 한 줄).
- **다시 켜기: 두 단계, 모두 root (판정 10)**: ① §5.3의 ack(래치 ID·항목별 결속)로 **래치를 해제**하고, ② **root가 킬스위치 파일을 직접 수정**해 `auto_select_off`를 내립니다(`deploy-*`는 PAUSE 해제로 끝나 ②가 없음). OR 병합이므로 ①만 하면 파일이 그대로 켜져 있고, ②만 하면 런처가 래치를 다시 반영하므로 **둘 다** 해야 툴이 켜집니다. `ctl/stop/`의 stop 파일이 남아 있으면 해제 직후 새 래치가 생기므로, **ack 전에 소유자가 stop 파일을 지웁니다**(파일을 지우는 것만으로는 효력이 풀리지 않습니다). NOPASSWD 없음.
- **위협**: 소유자 계정이 침해되면 서비스를 멈출 수 있습니다(서비스 거부). 켜는 것은 root라서 얻는 것이 없는 방향입니다.
- **한계 (판정 10, L-r4-3, 명시)**: 래치(`state/`)와 킬스위치 파일은 `_evegw` 쓰기 영역에 있어서 **침해된 gateway(`_evegw`)가 `state/` 래치와 킬스위치 파일을 지우거나 고칠 수 있습니다.** 지운 래치는 root ack 없이도 사라지므로, stop·PAUSE의 지속성은 gateway가 침해되지 않았다는 가정에 기댑니다. 소유자의 stop 파일이 남아 있는 동안은 런처가 다음 폴링에서 래치를 다시 만들지만, 같은 uid 한계(§3.4) 때문에 이것도 침해된 gateway를 막는 보장은 아닙니다. **킬스위치와 PAUSE는 운영 통제이지 보안 경계가 아닙니다.** 침해되지 않은 상태에서 5초 안에 멈추는 용도이고, 침해된 gateway를 막는 근거로 쓰지 않습니다(문서·테스트·보고 모두). 경계는 서명 검증·root 하한 floor·root 소유 ack입니다(§5.3, §6).
- **5초**: 런처 폴링 주기와 gateway의 킬스위치 폴링(2초 폴링, 5초 이내 반영, #158)의 합이 5초 안인지는 host 실측 항목입니다(§12 DevOps 18).

## 6. 위협 모델 요점

| 위협 | 현재 | 새 모델 (rev 2) | 잔여 위험 |
|---|---|---|---|
| CI 서명 파이프라인 침해 | 서명된 악성 릴리스가 root로 설치(검증기는 root) | 서명된 악성 릴리스가 **`_evegw`로** 실행 (root 아님) | 서비스 계정이 가진 것(시크릿·DB)은 노출. 신원 고정·태그 보호·14일·floor·PAUSE는 그대로 |
| `evedeploy` 침해 (poller·fetch·curl 응답·tar 해석의 버그) | root 승격 경로는 sudoers 4개 동사뿐이라 영향 제한 | `releases/`·`live/`·`staging/`을 쓸 수 있으나 **게이트는 런처뿐이고 서명되지 않은 코드(gateway·웹 모두)는 거부**. 상수·런처 우회 불가, 시크릿·DB 접근 없음 | 서비스 거부(링크를 깨뜨림·요청 위조) |
| **`live/previous` 교체로 서명된 구버전 되돌림** | (해당 없음: root 상태 파일) | **롤백 대상은 런처 소유 `last_good`·`prev_good`**. `evedeploy`가 링크·`releases/`를 바꿔도 대상이 안 바뀜. 신규 릴리스는 `last_good`보다 높아야 함 | `evedeploy`가 임의 구버전을 적용시킬 수 없음. 침해된 `_evegw`가 `state/`를 낮추는 경우만 root floor 하한까지 |
| 웹 정적 파일 변조 | root 소유 `web/` | `evedeploy`가 `releases/web`을 바꿔도 **런처가 검증한 `run/web` 사본만 서빙** | 침해된 gateway가 `run/web`을 고치면 다음 전환·재기동 전까지 유지(§12 주기 재해시 후보) |
| **`state/` 래치(PAUSE·stop·blocklist) 삭제** | (해당 없음: root 상태 파일) | 래치는 `_evegw` 쓰기 영역이라 침해된 gateway가 지울 수 있음. **운영 통제로만 취급**(§5.3) | 래치가 지워져도 서명 검증·root 하한 floor는 유지. 래치가 침해된 gateway를 막는다고 보지 않음 |
| sudoers 규칙 침해 | 있음 (4개 동사) | **규칙 자체가 없음** | 없음 |
| 코어 변조 | root 소유 | root 소유 + **상위 디렉터리까지 root 사슬, 런처가 시작 때 사슬 검사** | 변경은 사람 root 1회. 핀 sha256 + 보안 리뷰 |
| **env 주입 (`DYLD_*` 등)** | plist `EnvironmentVariables`(root 소유) | **root 소유 allow-list 밖의 키는 런처가 버림**. 보안 관련 키는 `_evegw` 소유 파일에서 읽지 않음 | allow-list 변경은 코어 변경(사람 root) |
| 검증 후 교체(TOCTOU) | root 전용 작업 디렉터리에서 검증 | 런처가 `run/` 사본을 만들고 **그 사본을 검증·해시한 뒤 실행**. 열린 fd에서 해시·실행(§3.4) | `_evegw`가 침해되면 사본 변경 가능, 재기동·전환 시 복구. 같은 uid 프로세스와의 경합은 §3.4 |
| **같은 uid 잔존 프로세스(런처=gateway uid)** | (해당 없음) | 사이클마다 잔존 프로세스 0개 확인, fd 해시·실행, 런처 hardened runtime·`PT_DENY_ATTACH`, 제어 소켓 없음(§3.4) | 실측(§12 DevOps 16)이 '가능'이면 런처 별도 uid `_evelaunch`가 S2 전제(MAJOR, 보안 재리뷰). 웹 사본 교체는 이 확인 대상 아님 |
| 서비스 계정 상승 | `_evegw`는 코드 수정 불가 | `_evegw`가 `run/`을 수정 가능 (코드는 `releases/`·`last_good`에 서명본으로 남음) | 약간 약해진 부분. 런처 재검증은 **다음 기동에 변조 사본이 쓰이는 것**을 막지만, **같은 uid의 살아남은 프로세스가 검증과 exec 사이에 `run/gw`를 바꿔칠 수 있어** 지속성을 완전히 차단하지는 못함(rev 4의 "지속성 차단" 문구 정정). 완화는 §3.4 |
| 시크릿 | `etc/` `_evegw` 0600 | 동일. `evedeploy` 접근 없음 | 동일 |
| 시계 오차 | `built_at` 검사 실패 → 거부 | `built_at`·trust root 만료·적용 지연이 모두 시계에 기댐 | **시계가 빨리 가는 경우만 안전**(거부 쪽). 느리거나 뒤로 가면 14일·만료 판정이 느슨해지고 지연이 줄 수 있음(§5.4). 경계는 서명·floor·blocklist·PAUSE |
| stop 요청 위조·남용 | (해당 없음) | `ctl/stop/`은 **비신뢰 입력**: `lstat`+`O_NOFOLLOW`+`fstat`, 일반 파일·소유 uid·정확한 이름만, 내용 안 읽음, 항목 수 상한. **'정지'로만 해석**, stop은 래치만 만들고 tools는 `auto_select_off` 하나, 단조 OR 병합(§5.5) | 서비스 거부(소유자 계정 침해 시). 켜는 것은 root(ack 해제 → root 수정). **침해된 gateway가 `state/` 래치와 킬스위치 파일을 지울 수 있음**: 킬스위치·PAUSE는 **운영 통제이고 보안 경계가 아님**(§5.3, §5.5) |
| ack 재사용·와일드카드 | (해당 없음: root가 직접 조작) | ack는 **래치 ID·항목별 결속**, `core/ack/`(root)에만 존재, 와일드카드 없음(§5.3) | 소유자 root가 침해되면 풀 수 있는 점은 그대로 |
| 적용 지연 소급 | (해당 없음) | `first_seen`이 `state/`에 있어 gateway 침해 시 소급 가능 → 지연은 **보조 통제**(§5.4) | 게이트는 런처의 서명·floor·blocklist·PAUSE |
| 슬롯 기록 변조(`built_at` 예외 악용) | (해당 없음) | 기록 sha 일치·재해시·서명 재검증·floor·blocklist·스키마·경보(§3.2 2) | floor 위 오래된 서명본(서명 없는 코드는 불가) |
| 시스템 전반 | 배포가 root 코드 실행 경로 | **배포 경로에 root 코드 없음** | 최초 설치와 코어 변경·브레이크 풀기·다시 켜기·시크릿 회전 때의 사람 root(§2) |

정리: **강해지는 점**은 배포 경로에서 root 코드 실행이 없어진다는 것(침해 시 최대 영향이 root → `_evegw`로 내려감)과, 롤백·브레이크 판정이 `evedeploy`가 쓸 수 있는 곳 밖에 있다는 것. **약해지는 점**은 root 전용 검증 디렉터리의 방어가 사라지고, 그 몫을 런처 재검증과 소유 경계가 대신한다는 것입니다. 이 균형은 보안 리뷰에서 **조건부로 채택**됐습니다(§13).

## 7. DevOps 초안 v2(티켓)와의 정합

초안 v2 기준(코어/페이로드 분리안은 DevOps가 취소함, 이 문서도 분리안에 기대지 않음).

- **같은 것**: 서비스 계정 권한 배포, `releases/<kind>/X.Y.Z` + 링크 원자 교체, 직전 1개 보관, 감시 wrapper 방식(R1)의 재시작, 서명 검증 순서 재사용, sudoers 제거, 최초 설치를 한 줄 명령에 포함, 처음 배포는 기존 검증된 root 경로로.
- **이 문서가 다른 것 (보안 리뷰에서 비교해 주세요)**
  1. **검증 로직 소유**: 초안 v2 기본안은 "서비스 소유(sudo 제로, 탈취 시 우회)". 여기서는 **신뢰 상수(sigstore-go 내장 런처·trust root·신원)는 root 소유·1회 설치(상위 디렉터리까지 root 사슬)**, **보조 검증은 `evedeploy`, 최종 게이트는 런처(`_evegw` 실행)** 로 둡니다. 이렇게 하면 `evedeploy` 침해로 신원 상수를 바꾸거나 trust root를 교체할 수는 없습니다(바이너리·웹 교체는 서명이 없어 런처가 거부). 대가는 신뢰 상수 변경이 여전히 사람 root라는 예외 1건입니다(§2). 보안 리뷰에서 채택됐습니다.
  2. **브레이크**: 초안 v2는 서비스 소유. 여기서는 조이기 **요청**(`evedeploy`)·끄기 요청(소유자, sudo 없음)·판정(런처가 흡수)·풀기와 다시 켜기(소유자 root, NOPASSWD 금지)를 나눕니다(§5.3, §5.5). ACL 가정은 쓰지 않습니다.
  3. **wrapper의 역할**: 초안 v2는 링크 변경 시 SIGTERM만. 여기서는 같은 wrapper(런처)가 **서명·해시 검증, 웹 사본 검증, 실패 시 `last_good`으로 계속 서비스**까지 맡습니다. 런처 코드가 커지므로 크기 제한이 필요합니다(§12 DevOps 3).
  4. **계정 분리**: 초안 v2의 B(설치 계정 ≠ 실행 계정)에 동의. 단 §6에 적었듯 `run/` 실행 사본이 `_evegw` 소유라는 점이 "서비스가 자기 코드를 못 바꾼다"를 약하게 합니다. 보안 리뷰는 이 구조를 조건(§3.1)과 함께 받아들였습니다.
- **초안 v2 질문에 대한 아키텍트 답**: (1) R1 wrapper는 launchd에서 `UserName=_evegw`, `KeepAlive` 조합으로 가능하다고 보지만 host 검증 전 가정(§12). (2) `static_dir`을 링크로 바꾸면 요청마다 디스크에서 읽으므로 호환(기존 `ui_ok` 주석 근거), 열린 파일은 링크 교체와 무관. **rev 2**: 링크는 `live/web`이 아니라 런처가 검증한 `run/web`입니다(§4, 보안 리뷰 High). (3) 환경 변수·플래그(`GW_*`, `GW_PIM_*`): rev 1의 "`etc/`의 `_evegw` 소유 env 파일로 옮기는 안"은 **폐기**했습니다(보안 리뷰 Med: 침해된 `_evegw`가 파일을 영속적으로 변조할 수 있고 `DYLD_*` 같은 주입에 악용될 수 있음). 대신 **root 소유 allow-list**를 둡니다: ① `core/`의 allow-list(root 사슬)에 있는 키만 자식에 전달하고, 값은 root 소유 plist·allow-list에 고정합니다. ② 런처는 자식 환경을 비운 뒤 allow-list 키로만 새로 만듭니다(상속 금지). ③ `DYLD_*`·`LD_*` 등 동적 로더 관련 키는 allow-list 포함 여부와 상관없이 항상 버립니다. ④ 보안 관련 키 분류(어느 키를 값까지 root가 고정하는지)는 S1에서 보안 담당과 확정합니다. 비보안 설정은 `etc/config.yaml`(기존)로 읽되 env로는 전달하지 않습니다. 변경은 코어 변경(사람 root)입니다. (4) 스키마가 다른 릴리스는 **거부 유지**(§5.2). (5) 테스트 이식은 S1에서 샌드박스 테스트를 먼저 옮겨 범위를 측정.
- **캘린더 어댑터(`/opt/eve-pim`, 사용자 `gui` 도메인 LaunchAgent, TCC)**: 이 문서 범위 밖입니다. 자동 경로에 넣지 않는다는 초안 v2·보안(#168)의 결정을 따릅니다.
- PR #168(one-shot 단계 추가)은 현 모델(sudoers 기반) 위의 변경입니다. S3에서 제거되는 것(sudoers 설치, `rehearse/unpause`의 sudo 경로)과 겹치지 않게 순서를 맞춥니다.

## 8. 단계별 이행 계획과 롤백, SemVer 판정

모든 단계는 자기 롤백 방법을 갖고, 앞 단계가 끝나 헬스·리허설이 통과한 뒤에만 다음으로 갑니다. **S2 이후는 보안 승인(S2 설치 직전 구현 리뷰) 없이 시작하지 않습니다.** 승인 경로는 아래 "승인 규칙"을 따릅니다. 설계 리뷰(제3안 채택)는 끝났지만 구현 리뷰는 따로 필요합니다.

| 단계 | 내용 | root | SemVer | 롤백 |
|---|---|---|---|---|
| S0 | 이 설계 확정(보안 리뷰 반영 rev 2, DevOps 정합). 코드 변경 없음. 보안 리뷰: S0 문서 머지는 보안상 문제 없음 | 없음 | PATCH (문서만) | PR 되돌리기 |
| S1 | `evedeploy` 권한 검증기·전환 스크립트와 **런처의 검증 로직을 드라이런 전용**으로 추가. 현 sudo 경로와 같은 입력으로 결과를 비교하는 테스트(샌드박스, 실제 서명 픽스처 재사용). **경로 사슬 검사기**, env allow-list 파서(DYLD 등 거부), 브레이크 요청 흡수 로직도 테스트로 먼저 확정. 보안 관련 env 키 분류를 보안 담당과 확정. (rev 3~4) **적용 지연 상태기계(§5.4, 보조 통제)**, **sigstore-go 대조 테스트(§5.1.1)**, `mv -fh` 링크 교체 테스트와 링크 대상 `mv -f`·`ln -sf` grep 점검, (rev 4) ack 결속·stop 해석기(§5.3, §5.5), `requests/` 읽기 권한 모델, kind별 `state/` 분리, trust root 만료 경보(§5.1.2), (rev 5) stop 입력 검증기·킬스위치 OR 병합(§5.5), 같은 uid 완화(§3.4: 잔존 프로세스 0개 확인, fd 해시·실행, `PT_DENY_ATTACH`), floor 하한 상향 절차(§5.3). host 동작 변화 없음 | 없음 | MINOR (추가만) | 파일 삭제 |
| S2 | **마지막 root 설치**: `core/`(런처·trust root·env allow-list·floor 하한·`ack/`)와 **상위 디렉터리까지의 root 소유 사슬**, `releases/`·`live/`·`state/{gateway,web}/`·`run/`·`ctl/stop/`·`staging/{dl,requests}/` 디렉터리, 새 plist(**`UserName=_evegw`로 런처 실행**), gateway의 **`static_dir`을 `run/web`으로**, 기존 `/opt/eve-gw/bin`·`web`은 **그대로 남겨 두고** 병행 설치. 전환은 plist 교체 1회. 단 §12 DevOps 16 실측이 '같은 uid에서 가능'이면 **런처 별도 uid(`_evelaunch`)가 S2의 전제**입니다(§3.4, MAJOR, 보안 재리뷰) | **1회** | **MAJOR** (보안 경계, 서비스 실행 경로, 설정 키 변경) | 이전 plist 복원 + 이전 `bin`·`web`이 남아 있어 즉시 복귀. 이 롤백은 root 필요(설치 단계의 롤백이므로 허용). **런처 설치 후 `last_good` 기록이 비어 있는 첫 기동은 "현재 서명 검증을 통과한 릴리스"를 승격 전 상태로 시작** |
| S3 | poller를 sudo 없는 경로로 전환(링크 교체 + 요청 파일 방식), **sudoers 규칙 제거**, `rehearse`(일부러 실패하는 릴리스)로 자동 롤백(`last_good` 복귀)·브레이크 흡수·소유자 끄기(sudo 없음)·풀기·다시 켜기(root, ack 결속) 경로 검증 | 규칙 삭제 1회 (또는 S2에 포함) | **MAJOR** (권한 모델 변경) | 규칙 재설치(root)로 구 경로 복귀. 구 엔트리포인트는 S4까지 남겨 둠 |
| S4 | 구 root 엔트리포인트·`uninstall` 정리, 런북·README·`eve-web/.github/workflows/release.yml` 머리말에서 `sudo` 문구 제거, `scripts/test`에서 sudo 테스트 정리. **"서명 없는 코드는 실행 안 됨" 문구는 S2의 `run/web` 전환이 검증된 뒤에만 문서에 쓴다** | 없음 | **MAJOR** (운영 인터페이스 삭제) | 이전 태그로 되돌림 |
| S5 (선택, 별도 PR) | PostgreSQL을 `_evepg`, mlx_lm을 `_evemlx` LaunchDaemon으로 분리, 백업을 `evebackup`으로 | **1회** (계정·데이터 이동) | **MAJOR** (데이터 소유 변경). **기존 데이터 디렉터리 삭제는 건영님 직접 승인**(이동은 보안 승인과 롤백 계획이 있으면 control plane 승인) | `pg_dump` 백업 복원, 기존 데이터 디렉터리는 확인될 때까지 삭제하지 않음 |

버전 선: 이 변경은 **#158(6.0.0)에 묶지 않습니다.** 어느 MAJOR 번호에 싣는지는 PM이 정합니다. 배포 도구는 gateway와 별개 버전 선이라 도구만 올리는 방법도 가능하지만, 불확실하므로 MAJOR로 둡니다.

**승인 규칙 (rev 5, 현재 규칙)**: MAJOR(S2~S5)는 ① **보안 승인**과 ② **롤백 계획**(위 표의 롤백 열과 리허설)이 모두 있으면 **control plane 승인으로 진행**합니다. 건영님 직접 확인이 필요한 것은 **데이터 삭제, 외부 공개, 비용, 외부 메시지** 네 가지뿐입니다. rev 4까지의 "S2~S5는 건영님 직접 확인" 문구는 이 규칙으로 대체합니다. 이 문서의 단계 중 해당 가능성이 있는 것은 S5의 기존 데이터 디렉터리 삭제뿐입니다.

각 단계 공통 완료 조건: (1) CI 통과, (2) 보안 리뷰, (3) S2·S3·S4·S5(MAJOR)는 보안 승인과 롤백 계획이 있는 상태에서 control plane 승인(위 승인 규칙), (4) 샌드박스 테스트에 "서명 실패·변조·다운그레이드·blocklist·PAUSE 해제 시도"에 더해 rev 2 항목이 포함됨:
- `live/previous-*`·`releases/` 교체로 구버전을 적용시키려는 시도가 거부됨(롤백 대상은 `last_good`뿐)
- `releases/web` 변조 → `run/web`이 바뀌지 않음, 런처가 실패 처리하고 이전 `run/web` 유지
- 경로 사슬 구성 요소 하나라도 root 소유가 아니거나 group·other 쓰기 가능이면 런처가 기동을 거부
- 런처가 root가 아닌 `_evegw`로 실행되는지 확인(`ps`·plist 점검)
- allow-list 밖 env 키(`DYLD_INSERT_LIBRARIES` 포함)가 자식에 전달되지 않음
- `evedeploy`가 요청 파일을 지워도 PAUSE·blocklist·floor 효력이 유지됨
- 런처 검증이 배포 시점 검증보다 느슨한 항목이 없음을 비교하는 테스트(같은 픽스처로 둘의 거부 결과 대조)
- (rev 3) 지연 중에 서명된 새 릴리스가 와도 `last_good`이 유지되고, 재부팅 뒤에도 후보가 실행되지 않음. `apply_after` 뒤 재검증에서 변조를 잡음. PAUSE는 적용을 막고 롤백은 지연되지 않음. **이것은 보조 통제의 동작 확인**이고, 지연이 깨져도(예: `first_seen` 소급) 서명·floor·blocklist·PAUSE 게이트가 막는지를 별도로 확인
- (rev 3) 링크 교체 뒤 `releases/` 안으로 임시 링크가 생기지 않음. 런처가 `releases/` 복사에서 심볼릭 링크·특수 파일·한도 초과를 거부함
- (rev 3) 배포 경로에 root로 실행되는 프로세스·LaunchDaemon이 없음(plist 목록 점검)
- (rev 4) `prev_good`·`last_good` 슬롯 사용 시 기록 sha 불일치·사본 변조·서명 불량·floor 미달·blocklist·스키마 불일치 중 하나라도 있으면 거부, 정상 사용에는 경보가 남음
- (rev 4) ack: 다른 `latch_id`·다른 항목·재사용·와일드카드 ack는 무시됨, sudoers에 NOPASSWD 항목이 없음
- (rev 4) stop: 목록 밖 scope·형식 위반은 무시, '정지'로만 해석, 요청 파일을 지워도 래치 유지, 다시 켜기는 root ack 없이는 불가
- (rev 4) 런처(`_evegw`)가 `staging/requests/`는 읽고 `staging/dl/`·`poll/`은 읽지 못함, `evedeploy`가 쓸 수 없는 곳은 쓰지 못함
- (rev 4) `state/gateway/`와 `state/web/`이 독립: 한 kind의 PAUSE·floor·blocklist·슬롯이 다른 kind에 영향을 주지 않음
- (rev 4) sigstore-go: 네트워크 차단·trust root 손상·번들 없음·의존성 오류에서 fail-closed이고 폴백 경로가 없음(`core/`에 cosign 없음). trust root 회전 리허설에서 구 항목 유지 시 `last_good`·`prev_good` 재검증 통과
- (rev 4) 런처와 gateway가 같은 uid인지 점검하고 그 영향의 실측 결과를 확인(§12 DevOps 16)
- (rev 5) stop 입력: 디렉터리(`ctl/`·`ctl/stop/`)의 소유·모드·링크 검사 실패는 요청 없음과 경보, 심볼릭 링크·디렉터리·특수 파일·다른 소유 uid·`stop-(tools|deploy-gateway|deploy-web)` 밖 이름은 무시, 읽을 수 없는 항목은 요청 없음, 항목 수 상한 64 초과는 64개까지만 보고 경보, 내용이 있든 없든 결과가 같음(내용을 읽지 않음), 검증과 열기 사이 파일을 바꿔치기해도 `fstat`에서 거부
- (rev 5) stop 해석: tools scope는 `auto_select_off`만 바뀜(다른 키 불변), 킬스위치 파일 갱신은 OR 병합(켜진 값을 내리는 경로 없음), 임시 파일 + `rename(2)`, 0600 `_evegw`, 깨진 파일은 덮어쓰지 않고 경보만. 래치 해제(ack)만 하거나 파일 수정만 해서는 툴이 켜지지 않고 둘 다 해야 켜짐. ack 전에 stop 파일을 지우지 않으면 새 래치가 생김
- (rev 5) 같은 uid: 사이클 시작 전 잔존 `_evegw` 프로세스(`setsid` 자식 포함)가 있으면 적용 보류·경보이고 0개 확인 뒤에만 검증, 열린 fd에서 해시·실행(구현 불가 시에도 **0개 확인 → 검증 → exec 직전 inode·크기·mtime 재확인** 순서를 지키고, 값이 다르면 실행하지 않음), 런처의 hardened runtime·`PT_DENY_ATTACH` 점검, 런처↔gateway 제어 소켓이 없음, 샌드박스에서 `run/gw` 바꿔치기 경합 시험
- (rev 5) 래치 삭제: `state/` 래치·킬스위치 파일을 지운 시험에서 서명 검증·root 하한 floor는 유지되고, 문서·보고에 래치가 보안 경계로 쓰이지 않음
- (rev 5) floor 하한 상향: 새 값 이상 릴리스를 먼저 승격하지 않으면 절차가 거부됨을 점검, 하한 교체는 임시 파일 + `rename(2)`이고 사슬 조건 유지. 풀기 도구가 `core/`의 root 소유 스크립트이고 쓰기 가능한 경로의 도구는 sudo로 실행되지 않음

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
| trust root·런처(sigstore-go 내장)·신원 상수·**env allow-list·floor 하한·ack** | root | `core/` (상위 디렉터리까지 root 사슬) | 읽기만 | 읽기만 | 핀 sha256. 런처는 `_evegw`로 실행 |
| 웹 정적 파일 사본 | `_evegw` | `run/web` | 접근 없음(`releases/web`에 원본 배치만) | 읽기·쓰기 | 런처가 검증 복사 |
| 롤백 슬롯·브레이크 기록 (kind별: gateway, web) | `_evegw`(런처) | `run/last_good/`·`run/prev_good/`·`state/gateway/`·`state/web/` | 읽기만 | 읽기·쓰기 | 롤백 대상은 여기만(§5.2) |
| 조이기·롤백 요청 | evedeploy | `staging/requests/` 0755(파일 0644), 상위 `staging/`·`/var/evedeploy/` 0711 | 쓰기 | 읽기만 | 비밀 없음. `dl/`·`poll/`은 0700으로 `_evegw` 접근 없음 |
| 끄기 요청 | 건영(owner) | `ctl/stop/` 0755(파일 0644) | 접근 없음 | 읽기만 | sudo 없이 소유자가 씀. '정지'로만 해석(§5.5) |
| 일회용 임시 토큰 (one-shot) | 건영(owner) | 환경 변수에서만 | 없음 | 없음 | PR #168 조건 그대로 |

점검 항목(비root로 확인 가능한 것만): 각 디렉터리 `ls -ld` 소유·모드(`/var/evedeploy`·`staging/` 0711, `requests/` 0755, `dl/`·`poll/` 0700 포함), **`core/`·plist 경로의 모든 구성 요소가 root 소유이고 group·other 쓰기 불가**(§3.1), 런처 프로세스가 `_evegw`로 실행 중, 서비스 그룹 멤버십 비어 있음, `sudo -n -l -U evedeploy` 에 NOPASSWD 없음(S3 이후).

## 10. 시크릿 회전은 어떻게 하나

R13의 `sudo -e etc/config.yaml`, api_key 교체는 지금 root입니다. 소유자 계정이 `etc/`에 접근해야 하는데 `etc/`는 `_evegw` 0700입니다. 후보:
- (a) 회전 도구를 `_evegw` 권한 서브커맨드로 제공(예: `gateway rotate-key`). 소유자 계정이 `_evegw` 전용 로컬 관리 소켓(0600)으로 요청. gateway 코드 변경이 필요해 별도 설계(소유자↔gateway 소켓이며 §3.4의 런처 제어 소켓과 다른 것입니다. 이 안을 택할 때는 같은 uid 논점, 보안 판정 11을 다시 봅니다).
- (b) 회전은 root 수동 작업으로 남기고 **코어 변경과 같은 예외**로 취급합니다(§2의 root 수동 작업 목록 #5에 포함: "root는 최초 설치만"이 아님).
이번 범위(배포)에는 포함하지 않고 (b)를 기본으로 둡니다. 소유자 sudo 제로 목표와는 별개 티켓으로 두자는 의견입니다.

## 11. 하지 않는 것 (범위 밖)

- gateway 요청 경로·API 계약·툴 선택 정책 변경 없음
- 서명 신원 정책 변경 없음(§5.1에서 그대로)
- 데이터 이동·삭제 없음(S5는 별도 PR. 데이터 삭제는 건영님 직접 승인)
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
12. (rev 2) `run/web` 링크 교체를 gateway가 요청마다 올바르게 따라가는지(`static_dir`이 링크를 가리킬 때 열린 파일·디렉터리 캐시 동작) 확인. **rev 3 코드 조사 결과**(`internal/handlers/server.go`, `static.go`): `static_dir`은 시작 시 `os.Stat`(링크를 따라감)으로 존재를 확인하고, 요청마다 `http.Dir(root)`로 열며 root **아래** 구성 요소만 `Lstat`로 링크를 거부합니다. 그래서 `run/web` 자체가 링크여도 동작하고, 원자 교체하면 다음 요청부터 새 트리를 읽습니다. 주의 둘: ⓐ UI 제공 여부(`uiServed`)가 **시작 시 고정**되므로 런처가 자식을 띄우기 전에 `run/web`을 먼저 만들어야 합니다. ⓑ 교체 순간에 한 번의 페이지 로드에서 `index.html`과 JS가 신·구 혼합될 수 있습니다(eve-web ES 모듈 전환의 캐시 쟁점과 같은 계열). 실제 host 동작 확인만 남습니다.
13. (rev 2) 런처가 `/Library/LaunchDaemons`의 plist와 `/opt/eve-gw/core/` 사슬 구성 요소를 `lstat`할 때, host의 기본 소유·모드(`/opt` 등)가 root 사슬 조건을 이미 만족하는지. (rev 3) 측정 대상에 **`/`, `/opt`, `/opt/eve-gw`, `/Library`, `/Library/LaunchDaemons`, plist 파일, 그리고 지금 코어가 있는 `/usr`·`/usr/local`·`/usr/local/libexec/eve`**를 모두 넣고, 각 구성 요소의 소유·모드·**ACL·xattr·flags**와 심볼릭 링크 여부를 기록(§3.1의 `/var`·`/etc` 주의 포함).
14. (rev 3) 런처가 `releases/`에서 `run/`으로 복사한 바이너리(Go, Apple Silicon)가 코드 서명(ad-hoc)을 유지한 채 launchd에서 실행되는지, quarantine xattr이 붙지 않는지. 현재 gateway 바이너리·웹 트리의 **크기와 파일 수, 해시에 걸리는 시간**(복사 한도와 재시작 지연 추정, §3.3).
15. (rev 3) FileVault 상태(`fdesetup status`)와, 현재 gateway LaunchDaemon의 프로세스 시작 시각이 부팅 시각 직후이고 로그인 시각보다 앞서는지(읽기 전용 증거). Q7의 실제 재부팅 시험은 별도 승인 단계.
16. (rev 4, rev 5 확장) **런처와 gateway의 uid와 같은 uid에서 닿는 범위**: 런처 프로세스와 gateway 자식이 모두 `_evegw`(uid 451)로 도는지(`ps -o user,uid,pid,ppid,command`와 plist `UserName`), 같은 uid라서 gateway가 런처에 닿을 수 있는지: 시그널(STOP·KILL), 프로세스 attach(`task_for_pid`·`ptrace`·디버거, hardened runtime·SIP 상태, `codesign -d --entitlements`), 열린 fd·환경 접근, `run/`·`state/` 쓰기 범위. **(rev 5 추가) ⓐ `setsid`로 떼어낸 자식이 런처가 자식을 종료한 뒤에도 살아남는지(§3.4 완화 1), ⓑ `ptrace`·`task_for_pid`가 같은 uid의 런처에 되는지(hardened runtime·`PT_DENY_ATTACH` 적용 전후), ⓒ fd 상속: gateway가 런처의 열린 fd(사본·상태 파일)를 상속·접근할 수 있는지.** 먼저 읽기 전용 증거(프로세스 목록·plist·코드 서명 플래그·엔타이틀먼트)를 남기고, 시그널·attach·`setsid`·fd 시도는 별도 승인 단계의 시험 계정에서만. **결과가 '같은 uid에서 가능'이면 런처 별도 uid(`_evelaunch`)를 S2의 전제로 올립니다**(설계 변경, MAJOR, 보안 재리뷰, §3.4). macOS `fexecve` 부재로 §3.4 완화 2(열린 fd에서 실행)가 구현되는지는 **S1 실측**으로 확인합니다(보안 담당 수용). 필수 순서(0개 확인 → 검증 → exec 직전 inode·크기·mtime 재확인)로 검증↔exec 경합이 실제로 가능한지 측정하고, **가능하면 `_evelaunch` 분리가 S2의 전제**입니다(MAJOR).
17. (rev 4) **trust root·cosign 만료 실측**(§5.1.2): 지금 설치된 `trusted_root.json`의 항목별 유효 시작·종료 시각과 설치된 cosign의 핀 버전(읽기 전용, 값만 기록).
18. (rev 4) **끄기 5초**(§5.5): 런처의 stop 폴링 주기와 gateway의 킬스위치 폴링을 합쳐 소유자 요청부터 툴이 꺼질 때까지 5초 안인지(런타임 시험, 별도 승인 단계).
19. (rev 4) **`requests/` 권한 모델**(§4): `/var/evedeploy`·`staging/` 0711, `requests/` 0755, `dl/`·`poll/` 0700에서 `_evegw`가 요청만 읽고 나머지는 못 읽는지, `evedeploy`가 그룹이 아닌 other 읽기만으로 충분한지(`ls -ld`·`stat`, 읽기 전용).
20. (rev 5, L-r4-4) **시간 동기(NTP) 상태**(읽기 전용, §5.4): 네트워크 시간 사용 여부, 사용 중인 서버 종류(호스트 이름은 기록하지 않음), 마지막 동기 시각과 현재 오차. 시계가 느린 경우의 영향이 `built_at`·만료 판정에 있기 때문입니다.

### 보안 담당 (rev 1 질문은 §13.1의 리뷰 결과로, rev 3까지의 질문은 §13.2의 판정으로 답변됨. 남은 확인만)

1. ~~채택된 것의 확인(§3.1)~~ rev 4: 판정 ④로 **세 분류로 확정**(§3.1). 소유자 stop 디렉터리의 취급만 10에서 확인.
2. ~~승격된 `last_good` 재기동의 `built_at` 생략~~ rev 4: 판정 ②로 **의도된 예외**, 조건 4개(§3.2 2).
3. ~~브레이크 풀기의 root 사용~~ rev 4: 판정 ③으로 **확정**, NOPASSWD 금지, ack 결속(§5.3).
4. `run/web`·`run/gw` 사본을 침해된 `_evegw`가 고칠 수 있을 때, 주기적 재해시(예: 60초)를 런처에 넣을지 아니면 전환·재기동 때 재검증으로 충분한지.
5. 보안 관련 env 키 분류(§7 ④)와 allow-list 형식(값까지 고정할 키).
6. 승격 5분 관찰과 `evedeploy`의 롤백 요청(`last_good` 복귀로만 해석)의 서비스 거부 위험이 허용 가능한지.
7. ~~적용 지연(§5.4)~~ rev 4: 판정 7로 수용, **보조 통제**로 문서화.
8. ~~sigstore-go 내장과 의존성 승인~~ rev 4: 판정 8로 **sigstore-go 채택, cosign 폴백 삭제**(§5.1.1).
9. ~~킬스위치 운영 변경 경로~~ rev 4: 판정 9로 **끄기는 sudo 없이, 켜기는 root**(§5.5).
10. ~~소유자 `ctl/stop/`의 취급과 `tools` scope 구현 방식~~ rev 5: 판정 10으로 **수용**. 비신뢰 입력, `lstat`+`O_NOFOLLOW`+`fstat`·일반 파일·소유 uid·정확한 이름·내용 안 읽음·항목 수 상한, stop은 래치만, tools scope는 `auto_select_off` 하나, 킬스위치 파일은 단조 OR 병합, 다시 켜기는 두 단계(§5.5).
11. ~~런처와 gateway가 같은 uid(`_evegw`)인 구조의 잔여 위험~~ rev 5: 판정 11로 **조건부 수용**(§3.4). 완화 4개를 두고, DevOps 16 실측이 '가능'이면 런처 별도 uid(`_evelaunch`)를 S2의 전제로(MAJOR, 보안 재리뷰). 남은 일은 실측 뒤의 재리뷰뿐입니다.

### 12.1 열린 질문의 해결 방법 분류 (rev 3)

| 방법 | 질문 |
|---|---|
| 읽기 전용 실측(root 없이, 값·비밀은 기록하지 않음) | DevOps 13(사슬 소유·모드·ACL·xattr·flags), 14(크기·파일 수·코드 서명·quarantine), 15(FileVault·시작 시각), 16(uid·plist·코드 서명·엔타이틀먼트, 시도는 제외), 17(trust root 유효 기간·cosign 버전), 19(디렉터리 모드), 20(시간 동기 상태), 4(`ThrottleInterval` 설정값만), 9(`tailscale` 상태 읽기), 8(PostgreSQL 데이터 크기·버전) |
| 코드·문서 조사로 닫힘 | DevOps 3(Go 단일 바이너리로 결정: 보안 M-1), 10(MANIFEST 스키마), 11(스크립트 읽기), 12(위 조사 결과), 보안 5(env 키 분류) |
| 별도 승인 단계(런타임 시험, 읽기 전용 아님) | DevOps 4(실제 재시작 지연), 16(시그널·attach·`setsid` 생존·fd 상속 시도), 18(끄기 5초), 6(Metal), 7(재부팅 시험), 8(PG 이전 리허설), 14(복사본 실행 시험) |
| **비차단** | DevOps 1·2·5: 런처 방식은 launchd 도메인을 조작하지 않으므로, "non-root `kickstart`가 거부되는가"와 "LaunchAgent가 부팅 때 안 오르는가"의 답이 가정과 달라도 설계(C)는 바뀌지 않습니다(답이 달라지면 더 단순한 대안이 열릴 뿐). **`kickstart`를 실서비스에 시험하지 않습니다**(성공하면 재시작되므로 읽기 전용이 아님) |

## 13. 승인과 리뷰 상태

- **보안 리뷰(rev 1, 대상 head `a8fb855`): 설계 제3안 채택(조건부).** S0 문서 머지는 보안상 문제 없음. S2 이후는 구현 리뷰 후 진행. 머지·배포는 건영님이 정한 절차를 따릅니다.
- **보안 리뷰(rev 2, 대상 head `e6c59e7`): High·Med 없음, Low 8건.** S0 문서 머지는 보안상 문제 없음. 판정 ①~④·7~9를 내렸고(§13.2), rev 3(`2f4a6dc`, #171로 main에 머지됨)은 최종 head로 다시 확인한다고 했습니다. 이 판정과 미반영 Low를 **rev 4**가 반영했습니다(#174로 main에 머지됨).
- **보안 리뷰(rev 4, 대상 head `f665cc3`, #174): High·Med 없음, Low 6건.** 보안 10 수용, 보안 11 조건부 수용. 판정과 Low는 **rev 5**가 반영합니다(§13.3). 실측 결과가 '같은 uid에서 가능'이면 §3.4의 런처 별도 uid(MAJOR)로 보안 재리뷰를 받습니다. 머지·배포는 정해진 절차(§8 승인 규칙)를 따릅니다.
- 승인(rev 5, 현재 규칙): S2·S3·S4·S5(모두 MAJOR)는 **보안 승인과 롤백 계획이 있으면 control plane 승인으로 진행**합니다. **데이터 삭제, 외부 공개, 비용, 외부 메시지**만 건영님이 직접 확인합니다(§8 승인 규칙).
- 이 문서의 확정 순서: DevOps 초안과 정합 → 보안 리뷰(rev 1·2·4 완료) → PM 결정.

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

### 13.2 rev 2 리뷰(대상 `e6c59e7`) 판정과 rev 4 반영 위치

판정 번호는 보안 담당의 메시지 순서(①~④, 7~9)입니다. §12의 질문과는 ④↔보안 1, ②↔보안 2, ③↔보안 3, 7·8·9↔보안 7·8·9로 대응합니다(① `prev_good`은 §5.2에서 제안한 항목이라 §12 질문 번호가 없음).

| # | 구분 | 판정 내용 | rev 4 반영 |
|---|---|---|---|
| ① | 판정 | `prev_good` 허용. 사용 시 재해시·서명 재검증, floor·blocklist·스키마 확인, 경보 | §5.2, §3.2 2, §8 공통 테스트 |
| ② | 판정 | `built_at` 생략은 "같거나 더 엄격"에 어긋남(기록이 `_evegw` 쓰기 가능). 의도된 예외로 명시, 조건: 기록 sha 일치·재검증·floor·경보 | §3.2 2, §6 |
| ③ | 판정 | 풀기는 root. NOPASSWD 금지. ack는 래치 ID·항목별 결속 | §5.3, §2(목록), §9 점검, §6 |
| ④ | 판정 | 입력은 세 분류: root 사슬 / `evedeploy` 쓰기 영역 / `_evegw` 쓰기 영역(`state/`·`run/`). 뒤 둘은 비신뢰 | §3.1, §4, §9 |
| 7 | 수용 | 적용 지연은 **보조 통제**로 문서화(`first_seen`이 `_evegw` state에 있어 소급 가능) | §5.4, §6 |
| 8 | 권장 | sigstore-go, 버전 핀, 오프라인 trust root, **cosign 폴백 삭제**, 실패 시 fail-closed(굳이 두면 AND, OR 금지) | §5.1.1, §0, §4, §8 |
| 9 | 권장 | 끄기(조이기)는 sudo 없이. 소유자 stop 요청을 런처가 '정지'로만 해석. 다시 켜기는 root | §5.5, §2 |
| L1 | Low | `mv -fh`가 전체에 반영됐는지 점검 | §5.2(점검 결과), §3.2 4, §8 S1·공통 테스트 |
| L2 | Low | cosign·trust root 만료 처리 | §5.1.2, §12 DevOps 17 |
| L3 | Low | `requests/`가 `evedeploy` 0700이라 런처가 못 읽음 | §4(`staging/` 0711, `requests/` 0755), §9, §12 DevOps 19 |
| L4 | Low | 상태를 gateway와 web으로 나눔을 명시 | §0 9~11, §4, §5.2 |
| L5 | Low | 제목·§0 "root는 최초 설치만"이 §2·§5.3·§10(b)와 모순 | 제목, §0, §2(root 수동 작업 목록), §10 |
| L6 | Low | 런처=gateway 동일 uid 실측 항목 | §12 DevOps 16, 보안 11 |
| L7 | Low | §13 갱신 | 이 절 |

(Low 8건 중 위 6건이 rev 3까지 미반영이었고, 나머지는 rev 3에서 이미 반영됐다고 봅니다. 목록은 보안 담당 메시지의 "미반영 Low" 기준입니다.)

### 13.3 rev 4 리뷰(대상 `f665cc3`) 판정과 rev 5 반영 위치

| # | 구분 | 판정 내용 | rev 5 반영 |
|---|---|---|---|
| 10 | 수용 | `ctl/stop/`은 비신뢰 입력. `lstat`+`O_NOFOLLOW`+`fstat`, 일반 파일·owner uid·정확한 이름만, 내용 안 읽음, 항목 수 상한. stop은 래치만 생성. tools scope는 `auto_select_off` 하나. 킬스위치 파일은 단조 OR 병합(임시 파일+rename, 0600 `_evegw`, 깨진 파일은 경보만). 다시 켜기는 ack 래치 해제 → root 수정 두 단계. 한계 명시: 침해된 gateway가 `state/` 래치를 지울 수 있음 | §5.5, §3.1, §4(`ctl/stop/`), §6, §8 공통 테스트, §12 보안 10 |
| 11 | 조건부 수용 | 같은 uid의 살아남은 프로세스가 검증↔exec 사이에 `run/gw`를 바꿔칠 수 있어 §6 '지속성 차단' 문구 정정. 완화: 매 사이클 같은 uid 프로세스 종료 후 0개 확인, 열린 fd에서 해시·실행, hardened runtime+`PT_DENY_ATTACH`, 런처↔gateway 제어 소켓 없음(§4 `run/` 소켓 정의). 실측에 `setsid` 생존·`ptrace`/`task_for_pid`·fd 상속 추가. `fexecve` 부재는 S1 실측으로 수용, 단 필수 순서: 0개 확인 → 검증 → exec 직전 inode·크기·mtime 재확인. 실측에서 경합이 가능하면 런처 별도 uid `_evelaunch`가 S2 전제(MAJOR, 재리뷰) | §3.4, §6, §4(`run/`), §3.2, §8 S1·S2·공통 테스트, §12 DevOps 16·보안 11 |
| L-r4-1 | Low | `ctl/stop/` 강화: `lstat`+`O_NOFOLLOW`+`fstat`, 일반 파일, owner uid 상수, 이름 `stop-(tools\|deploy-gateway\|deploy-web)` 정확 일치, 내용 안 읽음, 항목 수 상한 64, 디렉터리 소유·모드 검사, 읽을 수 없으면 '요청 없음' | §5.5 입력 검증(1~6, 상한 64, 디렉터리 검사), §4, §8 공통 테스트 |
| L-r4-2 | Low | tools scope 매핑 문서 고정: `stop-tools` → `auto_select_off` 하나만 | §5.5 요청 표·해석 |
| L-r4-3 | Low | 침해된 gateway는 래치와 킬스위치 파일을 지울 수 있음. 킬스위치·PAUSE는 운영 통제이고 보안 경계가 아님 | §5.3, §5.5 한계, §6 |
| L-r4-4 | Low | 시계 문구(빨리 가는 경우만 안전)와 실측에 NTP 상태 추가 | §5.4, §5.1.2, §6, §12 DevOps 20, §12.1 |
| L-r4-5 | Low | floor 하한 상향 절차. 풀기 도구는 root 소유이고 변경은 코어 변경과 같음 | §5.3(floor 절차·풀기 도구), §2 |
| L-r4-6 | Low | `state/` 래치(PAUSE·stop·blocklist)는 삭제될 수 있음 | §5.3, §6, §8 공통 테스트 |
| 규칙 | 정정 | S2~S5 승인 문구를 현재 규칙으로: 보안 승인+롤백 계획이 있으면 control plane 승인, 건영님 직접 확인은 데이터 삭제·외부 공개·비용·외부 메시지만 | 머리말, §8 승인 규칙·S5, §11, §13 |

(L-r4-1~6은 보안 담당이 rev 4 head 재확인에서 낸 Low 6건의 번호입니다.)

### 13.4 개정 이력

- rev 1.1: DevOps 초안 v2와 정합.
- **rev 2**: 보안 리뷰 반영(위 표). 열린 질문은 §12에 새로 정리.
- **rev 3**: 아키텍트 확인(DevOps 구현 계획 대조). root 상주 데몬 불채택(§3.3), sigstore-go 권장(§5.1.1), 링크 교체 `mv -fh`(§5.2), 적용 지연 자리(§5.4), 열린 질문 보강·분류(§12, §12.1). **보안 확인 필요: §12 보안 7·8·9.** (rev 4에서 판정 반영)
- **rev 4**: 보안 판정 ①~④·7~9와 Low 6건 반영(§13.2). `prev_good` 사용 조건, `built_at` 예외 조건, ack 결속·NOPASSWD 금지, 입력 세 분류, 적용 지연의 보조 통제화, sigstore-go 단일 검증기(cosign 폴백 삭제)와 만료 처리, 소유자 끄기(§5.5), `requests/` 권한 모델, 상태 gateway·web 분리, 제목·§0 정정, 런처 uid 실측 항목. 보안 재확인은 rev 5에서 결과(High·Med 없음, Low 6)를 반영.
- **rev 5**: 보안 판정 10(수용)·11(조건부 수용)과 Low 6건(L-r4-1~6) 반영(§13.3). `ctl/stop/` 입력 검증·stop은 래치만·킬스위치 OR 병합·두 단계 다시 켜기와 한계(§5.5), 같은 uid 한계·완화·`_evelaunch` 조건(§3.4)과 §6 문구 정정, 시계 문구와 NTP 실측(§5.4, §12 DevOps 20), floor 하한 상향 절차와 풀기 도구(§5.3), 래치가 보안 경계가 아니라는 명시(§5.3, §5.5, §6), `fexecve` 부재 시 필수 순서(§3.4), S2~S5 승인 문구를 현재 규칙으로(§8). 설계 결론(제3안, 게이트는 런처)은 그대로. **다이어그램 변경 없음**(이 문서에 연결된 다이어그램이 없음).
