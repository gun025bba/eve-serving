# 음악 플레이리스트 관리 — 설계 요약 — **제안 · 미구현**

상태: 설계 초안(시스템 아키텍트), 건영 결정(2026-10-28 08:39·09:46 KST)·PM 결정(09:39 KST)·**보안 리뷰(High 2·Med 11·Low 5) 반영 완료(§13 추적표)**. 착수 시점: **M1(캘린더 연동·아침 브리핑) 이후**.

> **외부 노출 4가지와 곡 제목 기록은 승인됨 (건영 직접, 2026-10-28 09:46 KST)** — PM 채팅의 선택 카드에 건영이 직접 답했다(원문: "Apple 검색 허용, 외부 검색엔진 검색 허용, iCloud 동기화 허용, 노래 제목 기록 허용"). 허용된 것: ① `catalog_check`로 Apple에 아티스트·제목·국가코드·host 공개 IP 전달, ② SearXNG 경유 외부 검색엔진 질의(건영의 상위 아티스트·장르 기반), ③ iCloud 동기화, ④ 노래 제목을 스레드(대화 기록)에 보관. **추천 기록 자동 삭제(#83)는 계속 미승인·보류.** 각 기능 켜기는 여전히 별도 MAJOR 릴리스·기본 off(§9).

> **경계 원칙(인증 독립)**: 기능은 인증 방식에 의존하지 않는다. 툴·라우트는 미들웨어가 붙인 principal `{user_id, scopes}`(내부 `users.id`)만 보고 필요한 scope만 검사한다. 쿠키·세션·페어링·Bearer 구분은 인증 계층 안에만 둔다.

## 0. 결론 (5줄)
1. **$99 없이 가능한 길은 (a) host의 Music.app 자동화뿐이다.** 재생 횟수·마지막 재생일·건너뜀 읽기, 플레이리스트에 곡 추가(`duplicate`)·제거(`delete`)가 목표 동작이다. 읽기와 `duplicate`는 알려진 동작이고, **`delete`가 플레이리스트 항목만 지우고 라이브러리 곡은 남기는지는 미검증(PoC에서 확인)**이다(§7.1).
2. **(b) MusicKit/Apple Music API는 무료 계정으로 불가**(개발자 토큰·Media ID 모두 유료 Program 필요). 유료여도 웹 API는 재생수 미제공·플레이리스트 곡 삭제 미지원이라 이 기능에는 맞지 않다 → 청사진으로만 남긴다.
3. **카탈로그(라이브러리에 없는) 곡의 자동 추가는 불가**(공식 스크립트 명령 없음, UI 스크립팅은 취약). → "라이브러리 우선 추천" + "카탈로그 곡은 Apple Music 링크만 제안, 건영이 직접 라이브러리에 담으면 그다음 추가 카드"로 설계한다.
4. **제거 제안은 서버 규칙(재생일·추가일·별점)으로 결정하고, 모델은 추천 이유와 새 곡 후보만 만든다.** 후보는 라이브러리/카탈로그 검증을 통과해야 보여 주므로 곡 이름 환각이 사용자에게 닿지 않는다. 로컬 모델 + SearXNG로 충분하되 "검증 게이트"가 핵심이다.
5. **구조는 캘린더 어댑터(eve-pim) 패턴 그대로**: 별도 LaunchAgent 헬퍼 `eve-music` + 0660 소켓, 읽기 verb / 확인 카드 쓰기 verb, 주간 잡, 인앱 스레드. SemVer **MAJOR**(새 host 헬퍼·TCC Automation·쓰기 동기화). 쓰기는 **자동 실행 없음**: 목록 카드에서 건영이 곡마다 확인한 것만 실행한다. 모든 기능은 기본 off이고, **`music`·`writes`·`catalog` 켜기는 각각 별도 MAJOR 릴리스**다(§9).

## 0.1 건영 결정과 승인 상태 (2026-10-28 09:46 KST 반영)
출처 확인: 건영 본인 메시지·카드 답(2026-10-28 08:39·09:46 KST)으로 확인한 것, PM이 결정한 것(09:39 KST), 아직 승인이 없는 것을 구분한다. 승인 표기는 한 가지로 통일한다: **'승인됨 (건영 직접, 일시 KST)'**. 출처는 PM 채팅 기록으로 확인한 건영 본인의 입력이다 — 08:39 KST는 건영이 채팅으로 쓴 메시지, 09:46 KST는 PM이 띄운 선택 카드("음악 추천에서 허용할 것")에 건영이 직접 답한 것(머리말과 같은 출처)이다. PM·다른 에이전트가 전달한 것은 '승인됨'으로 쓰지 않고 'PM 결정'으로 쓴다.

| # | 결정 | 상태 | 근거 |
|---|---|---|---|
| 1 | 곡 추가·제거를 자동으로 하지 않는다. 주기적으로(하루 1회든 주 1회든) **넣을 곡 목록·뺄 곡 목록을 위젯 카드로** 보여 주고, 건영이 **곡마다 확인한 것만** 실행한다 | **승인됨 (건영 직접, 2026-10-28 08:39 KST)** | 건영 직접 메시지("…넣을 곡 리스트, 뺄 곡 리스트를 … 위젯 카드 형식으로 … 컨펌을 하면 그때 넣고 빼는 식으로"). 주기 기본값 주 1회는 PM 선택. iCloud 동기화는 아래 #2a에서 **승인됨 (건영 직접, 2026-10-28 09:46 KST)** |
| 2 | 추천을 위한 검색 허용: **Apple 검색**(카탈로그 확인, `itunes.apple.com` 전용 클라이언트)과 **외부 검색엔진 검색**(기존 SearXNG 경로) | **승인됨 (건영 직접, 2026-10-28 09:46 KST)** (원문: "Apple 검색 허용, 외부 검색엔진 검색 허용"). 이전의 PM 결정 '외부 호출은 `itunes.apple.com`만'은 **이 승인으로 대체**되었다 | 외부 검색엔진 질의는 기존 SearXNG 경로로 하고, **`catalog_check`는 보안 High-1에 따라 계속 `itunes.apple.com` 전용 고정 클라이언트**다(범용 HTTP·SearXNG 경로·도메인 정책 재사용 금지). 08:39 KST 건영 메시지("추천을 위해서는 … 검색이 가능해야겠지")도 같은 취지 |
| 2a | **외부 노출 3경로**: (a) `catalog_check` → Apple(아티스트·제목 문자열, 국가코드 KR, host 공개 IP; 취향 후보가 Apple에 쌓임) (b) SearXNG 보조 질의 ≤3/실행(상위 아티스트·장르 기반)이 SearXNG 중계 외부 검색엔진으로, 주간 잡이라 건영이 보지 않는 상태에서 나감 (c) 곡 추가·제거의 iCloud 동기화 | **승인됨 (건영 직접, 2026-10-28 09:46 KST)** (Apple 검색·외부 검색엔진 검색·iCloud 동기화 각각 허용) | 승인은 범위 허용이며 보안 조건은 그대로: `catalog`와 SearXNG 보조 질의는 **별도 토글**·기본 off·켜기 화면에 이 표 표시·사전 체크 금지, 각 켜기는 별도 MAJOR 릴리스 |
| 2b | **노래 제목 기록**(위젯 카드가 스레드 메시지로 저장되어 곡명·표시 수치가 대화 기록에 남음) | **승인됨 (건영 직접, 2026-10-28 09:46 KST)** (원문: "노래 제목 기록 허용") | 보관 범위·보호는 §6. 자동 삭제는 미승인이라 기본 보존. 메모리 추출·피드백 내보내기 제외는 보안 권고로 유지하되 **메시지·툴 결과 단위 표시**로 한다(§6; 이번 승인은 대화 기록 보관만 허용) |
| 3 | 도우미(`eve-music`)는 캘린더 어댑터와 **같은 한 줄 설치**에 넣는다 | **PM 결정 (2026-10-28 09:39 KST, 건영 승인 대상 아님)** | 설치 중 TCC 팝업은 건영이 host에서 직접 눌러야 하는 점은 그대로 |
| 4 | 관리할 플레이리스트는 **기능을 켤 때 고른다** | **PM 결정 (2026-10-28 09:39 KST, 건영 승인 대상 아님)** | 고르는 행위(선택)는 건영이 직접 한다 |
| 5 | 지난 추천 기록 **자동 삭제는 기본 off**, 켤 때 30일 설정 | **미승인 · 보류**([건영 승인 필요], 이슈 #83과 함께) | 30일 설정값도 승인 전까지 확정하지 않는다. 건영 본인 메시지에 없음 |
| 6 | 자동 삭제의 **삭제 승인은 나중에 따로** 받는다 | **미승인으로 남김** [건영 승인 필요] | 승인 전에는 삭제 잡을 구현·활성화하지 않는다 |
| 7 | 착수는 M1 다음 | 건영 요청 맥락과 일치(PM 전달) | – |

> 위 2·2a·2b는 건영 직접 승인이므로 구현의 전제가 된다(승인은 켜기 릴리스 승인을 대신하지 않는다).
> 위 5·6은 데이터 삭제 규칙상 건영 직접 승인이 필요하며 승인 전까지 구현·활성화하지 않는다. 항목 3·4는 PM 결정이라 건영 승인 대상이 아니다.

## 1. 방식 비교 (무료 가능 여부)
| 항목 | (a) Music.app 자동화 (AppleScript/JXA) | (b) MusicKit for Swift / Apple Music API / MusicKit JS |
|---|---|---|
| 비용·계정 | **무료**, 개발자 계정 불필요 | **유료 Program 필수**. MusicKit App Service는 App ID 설정(유료)이고, 개발자 토큰(ES256 JWT)은 Media ID+개인키+Team ID가 필요. 무료 Personal Team은 불가 |
| 플레이리스트 읽기 | `user playlist`, `tracks` 가능 | 웹 API `GET /v1/me/library/playlists` 가능 |
| played count / played date / skipped | **가능**(`played count`, `played date`, `skipped count`, `skipped date`) | Swift `Song.playCount/lastPlayedDate`는 있으나(앱·유료 전제), **웹 API는 미제공** |
| 플레이리스트에 곡 추가 | 라이브러리 곡만 `duplicate … to user playlist`. 모든 사용자 플레이리스트 가능 | `POST …/tracks`는 `canEdit` 플레이리스트만(앱이 만든 것 위주) |
| 플레이리스트에서 제거 | `delete track of user playlist` 시도 가능. **플레이리스트 항목만 지워지는지, 스마트/동적 플레이리스트에서 거부되는지는 미검증(PoC에서 확인)** | **불가**(DELETE/PUT 403 보고) |
| 카탈로그 곡 추가 | **직접 불가**(라이브러리에 먼저 있어야 함). `open location`+System Events UI 클릭은 가능하나 취약 → 채택 안 함 | 카탈로그 검색·`POST /v1/me/library`로 추가 가능(토큰 전제) |
| 카탈로그 검증(환각 방지) | 해당 없음 → **iTunes Search API**(무료, 키 없음, 약 20회/분)로 별도 검증 | 카탈로그 검색 API |
| eve-ios 사용 | 불필요(host에서 처리) | eve-ios는 무료 Personal Team/SideStore → **MusicKit 불가** |
| 운영 제약 | **로그인된 GUI 세션의 LaunchAgent** + TCC Automation 1회 승인(건영 직접) | 해당 없음 |
| 평가 | **채택** | **M1 이후 범위에서 제외**, `MusicSource` 어댑터 청사진만 |

### 근거 URL (직접 열람 / 검색 요약 구분)
- Music.app 속성·플레이리스트 동작(검색 요약): https://github.com/sundial-org/awesome-openclaw-skills/blob/main/skills/apple-music-2/SKILL.md , `delete`가 플레이리스트 항목만 지운다는 출처 주장(**미검증(PoC에서 확인)**): https://dougscripts.com/itunes/pdf/removeduplicates.pdf
- 카탈로그 곡은 라이브러리 경유 필요 / 구독 곡 `duplicate`는 "library source"로만: https://apple.stackexchange.com/questions/304362/applescript-how-to-add-currently-playing-apple-music-song-to-a-playlist , https://developer.apple.com/forums/thread/694200
- 라이브러리 추가 UI 스크립팅(취약) 사례(열람함): https://apple.stackexchange.com/questions/467879/how-do-you-add-an-apple-music-track-to-your-library-with-applescript
- iCloud 클라우드 트랙은 AppleScript에서 shared track으로 보이고 재생수·별점은 클라우드로 전파되는 듯하다(저자 정정): https://dougscripts.com/itunes/2011/11/some-itunes-match-applescripting-notes/
- 재생수 동기화 불안정(iPhone↔Mac): https://www.theodorehq.com/echo/blog/posts/apple-music-play-count-not-syncing-mac
- macOS 26에서 `current track`이 카탈로그 재생에 실패(-1728): https://stackoverflow.com/questions/79770206/apple-music-scriptingbridge-broken-in-macos-tahoe-26 → **우리는 `current track`을 쓰지 않는다**(라이브러리/플레이리스트 조회만).
- LaunchAgent의 Automation(TCC) 권한은 실제 실행 바이너리에 귀속, 데몬/비GUI 세션은 -1743: https://github.com/qwibitai/nanoclaw/issues/2088 , https://apple.stackexchange.com/questions/202183/how-to-run-applescript-scpt-as-laucndaemon-by-root , https://chrispaynter.medium.com/what-to-do-when-your-macos-daemon-gets-blocked-by-tcc-dialogues-d3a1b991151f
- 유료 Program 필요(열람함): https://developer.apple.com/documentation/applemusicapi/generating-developer-tokens.md , 공식 지원 표(열람함): https://developer.apple.com/support/app-capabilities/ , 포럼 답변(검색 요약): https://developer.apple.com/forums/thread/120537 , https://developer.apple.com/forums/thread/706861
- MusicKit JS도 개발자 토큰 필요(검색 요약): https://developer.apple.com/help/account/capabilities/create-a-media-identifier-and-private-key/
- Personal Team 한계(검색 요약): https://developer.apple.com/help/account/basics/about-your-developer-account/
- 웹 API 재생수 미제공·플레이리스트 곡 삭제 불가(**포럼 직접 열람 403 → 검색 요약만, 미검증**): https://developer.apple.com/forums/thread/722974 , https://developer.apple.com/forums/thread/107807
- iTunes Search API(무료·키 없음·약 20회/분·403): https://performance-partners.apple.com/search-api , https://developer.apple.com/forums/thread/66399

> 미확인 항목은 PoC(이슈 1)에서 host 실측으로 닫는다: 대형 라이브러리 속도, `smart`/편집 가능 판별, iPhone 재생이 Mac `played date`에 반영되는지, macOS 26에서 `duplicate`/`delete` 동작.

## 2. 레이어 배치 (M1 패턴 재사용)
- **새 컴포넌트 1개**: host의 `eve-music`(Swift, **건영 로그인 세션 LaunchAgent**; 보안 담당 확인: 그룹 `_evepim` 공유는 허용). 캘린더 어댑터와 **같은 규칙**: 소켓 `<user-run-dir>/music.sock` 0660, 그룹 `_evepim`(사람·에이전트 없음, `_evegw`만), 디렉터리 0750 고정 소유자, 기동 시 기존 파일·심볼릭 링크 `lstat` 거부, `getpeereid`로 peer uid=`_evegw` 정확 일치, 고정 스키마·**요청 8 KiB 상한**·모르는 필드/메서드 거부, **네트워크 없음**(강제 수단은 아래 '헬퍼 강화'), **로그에 곡·플레이리스트 이름 금지**(개수·id만).
- 왜 eve-pim과 **별 프로세스**인가: TCC 권한이 바이너리별(Automation→Music vs 캘린더)이고, 킬 스위치와 폭발 반경을 분리하기 위해서다. 소켓 서버·peer 검사·스키마 검증은 공용 Swift 패키지(`EveHelperKit`)로 공유한다(코드 중복 방지).
- **AppleScript 주입 방지**: 헬퍼는 **고정 스크립트 템플릿**만 실행하고 값은 문자열 결합이 아니라 인자(`on run argv`)로 넘긴다. id는 `^[0-9A-F]{16}$`, 검색어는 길이 ≤64로 검증한다. 스크립트에 `play` 계열 동작은 없다. 검색어는 제어문자를 거부한다. **스크립트 템플릿 파일의 허용 단어 검사 테스트**로 `play` 계열·파일 접근·`do shell script`·`System Events`가 한 줄도 없음을 고정한다(PoC 프로브 `scripts/poc/music-probe.sh`가 같은 방식의 자체 점검을 쓴다). **[Med-9]**
- **헬퍼 강화 [Med-9, Med-10, Low-3, Low-4]**:
  - '네트워크 없음'은 선언이 아니라 **App Sandbox(네트워크 entitlement 없음) 또는 `sandbox-exec` 프로파일**로 강제하고, 코드 리뷰에서 `URLSession`·소켓 사용이 없는지 검사한다. AppleScript는 `osascript` 자식 프로세스보다 **인프로세스(`NSAppleScript`/ScriptingBridge)**를 쓴다(인자가 `ps`에 안 보임). 부득이 `osascript`면 `/usr/bin/osascript` 절대경로, 빈 env, 셸 없이 argv 직접 실행.
  - **설치 경로**: 바이너리는 root 소유(`root:wheel`, 0755) 경로, plist는 `/Library/LaunchAgents`. 사용자 쓰기 가능 경로(`~/Library/LaunchAgents`, `~/bin`)는 금지(건영 계정의 다른 프로세스가 바이너리·plist를 바꿔 Automation 권한을 쓰는 것을 막음). eve-pim 설치 규칙과 같은지 확인하고 같은 한 줄 설치에서 처리한다(이 경로는 설치 시 관리자 권한이 필요할 수 있음 — 구현 이슈 #84에서 확정).
  - **Music.app을 헬퍼가 실행하지 않는다**: 호출 전에 실행 여부를 확인하고 꺼져 있으면 `music_unavailable`(Apple Events가 앱을 자동 실행하는 부작용 방지). 그래서 제안·쓰기 때 건영의 Music 앱이 켜져 있어야 한다(§8 리스크). 호출마다 타임아웃, 동시성 1.
  - **서명**: 무료 계정은 ad-hoc 서명이 되기 쉽고 TCC가 빌드마다 바뀌는 코드 해시에 묶여 재빌드 때마다 재승인이 필요할 수 있다(PoC 확인). 로컬 자체 서명 인증서(로그인 키체인)로 안정적인 서명 요건 + Hardened Runtime, 재서명·`tccutil` 복구 절차를 런북에. Automation 승인은 Music 전체 제어 단위이고 verb 단위로 좁혀지지 않으므로 안전성은 서명된 바이너리의 고정 verb와 위 설치 경로 보호에 달려 있다.
  - `EveHelperKit` 추출은 **별도 PR로 리뷰**하고 eve-pim 테스트(peer 불일치·심볼릭 링크 거부·8 KiB 상한)가 그대로 통과해야 한다. Swift 서드파티 의존성은 0, 있으면 `Package.resolved` 고정.
  - 킬 스위치·읽기 전용 강등은 `writes_enabled`·`catalog.enabled`와 **독립으로** 동작하는지 테스트한다.
- **설치**: 캘린더 어댑터와 **같은 한 줄 설치**에 넣는다(두 LaunchAgent 설치, 권한 팝업 두 개를 건영이 한 자리에서 처리). 기능 켜기 전에는 헬퍼가 떠 있어도 툴은 노출되지 않는다.
- **TCC**: Automation(Music) 팝업은 GUI 세션에서 건영이 직접 1회 승인한다. 바이너리를 다시 서명하면 재승인이 필요할 수 있으므로 **고정 서명 ID**로 배포한다(런북에 `tccutil` 복구 절차). 로그아웃 상태면 헬퍼가 없으므로 `music_unavailable`로 처리한다(M1과 동일).
- **gateway**: 기존 툴 레지스트리·`pending_actions`·`scheduled_jobs`·인앱 선톡 전달 재사용. 카탈로그 검증(`music.catalog_check`)은 **헬퍼가 아니라 gateway의 전용 고정 클라이언트**가 한다(아래 §2.1). **범용 HTTP 클라이언트·SearXNG 경로·기존 도메인 정책(`internal/policy/domains`)은 재사용하지 않는다.** **기본 off.**
- **UI/iOS**: 웹 UI의 스레드·카드로만 표시. eve-ios는 변경 없음(푸시 없음, 인앱 전달만). 카탈로그 링크는 `music.apple.com`만 허용하고, eve-ios 셸(#64)에서는 앱 내 WebView 네비게이션이 아니라 **시스템 브라우저로 여는 명시적 핸들러**를 쓴다(#64의 '그 밖의 네비게이션 거부' 조건과 충돌하지 않게) **[Med-5]**.
- **관리 대상은 opt-in, 켤 때 고른다**: 기능 켜기 화면이 `music.list_playlists`(이름·개수만) 목록을 보여 주고 건영이 고른 것(최대 20)이 `music.managed_playlists`(persistent id)가 된다. **1개 이상 고르지 않으면 켜지지 않는다.** 그 플레이리스트만 Eve가 곡을 읽고 바꾼다.

### 2.1 `catalog_check` 전용 클라이언트 사양 [High-1] (구현 전 필수)
기존 `internal/policy/domains`는 denylist 기본 모드·와일드카드/접미사 규칙의 웹 검색/fetch용 정책이라 '정확 일치'가 보장되지 않는다. `catalog_check`는 그 정책·설정과 **무관한 코드 상수 고정 클라이언트**다.
1. 호스트 `itunes.apple.com`, 스킴 `https`, 포트 443, 경로 `/search`는 **코드 상수**. 설정·env·settings·툴 인자로 바꿀 수 없고 와일드카드·IP 리터럴 없음. 호스트 추가는 MAJOR.
2. 모델·사용자 입력은 **쿼리 파라미터 값에만** 들어가며 `url.Values`로 인코딩한다. 허용 파라미터는 `term`, `media=music`, `entity=song`, `country=KR`, `limit≤5`뿐. URL 문자열 결합 금지.
3. **리다이렉트 거부**(`CheckRedirect → http.ErrUseLastResponse`), 3xx는 오류.
4. **타임아웃**: 연결 3 s, 헤더 5 s, 전체 8 s, TLS ≥1.2. 프록시 환경변수 무시(**코드에 `Proxy: nil`을 명시**하고, `HTTP_PROXY`·`HTTPS_PROXY`·`ALL_PROXY`가 설정된 상태에서도 직접 연결되는지 테스트), 쿠키 jar 없음, `Authorization`/`Cookie` 헤더 없음, User-Agent 고정.
5. **응답 크기 상한 256 KiB**(`io.LimitReader`), 초과 시 파싱하지 않고 오류. `Content-Type`은 json·javascript 계열만(실제 값은 PoC에서 실측).
6. **사설 IP·DNS rebinding 차단 — 판정은 `net.Dialer.Control`에서 한다** (이름 해석 결과를 미리 검사하는 것은 보조일 뿐이다):
   - **`DialContext`는 `itunes.apple.com:443`으로 고정**한다. 요청의 `addr` 인자는 무시하고 코드 상수 호스트·포트만 dial한다(`Transport`가 다른 호스트·포트를 dial할 방법이 없음).
   - `Control(network, address, c)`에서 ① `network`가 **`tcp4` 또는 `tcp6`**일 때만, ② 포트가 **443**일 때만 허용하고, ③ `address`를 `netip.AddrPort`로 파싱해 **원본 주소와 `Unmap()` 결과를 모두** 판정한다. 둘 중 하나라도 비공개면 거부한다(v4-mapped IPv6로 사설 IPv4를 감싸 통과시키는 우회 차단).
   - 비공개 판정은 기존 `internal/policy/domains.IsNonPublic`(순수 함수)을 **호출**한다(**복사본을 만들지 않는다**; 도메인 정책·설정 계층은 쓰지 않는다). 그 위에 이 클라이언트 패키지의 **추가 거부 목록**을 OR로 적용한다: `64:ff9b:1::/48`(NAT64 로컬 용도), `100::/64`(discard), `2001:db8::/32`(문서용), TEST-NET-1·2·3(`192.0.2.0/24`, `198.51.100.0/24`, `203.0.113.0/24`), `192.88.99.0/24`(6to4 릴레이 anycast). 함수가 이미 거부하는 사설·loopback·link-local·CGNAT/tailnet(`100.64/10`, `fd7a:115c:a1e0::/48`)·6to4(`2002::/16`)·Teredo(`2001::/32`)·NAT64(`64:ff9b::/96`)·v4-mapped는 그대로 거부한다. 추가 목록은 `IsNonPublic` 자체에 넣을 수도 있으나 그것은 기존 정책 패키지의 변경이라 별도 PR·보안 리뷰로 한다.
   - 이름 해석(`LookupNetIP`) 결과는 **모든 답이 공개 IP**일 때만 쓰고, 검증된 IP 리터럴로 dial하며, 연결 시점에 `Control`이 다시 판정한다(rebinding 대비 TOCTOU 없음).
   - **'전용 복사' 선택지는 두지 않는다.** 분류 로직의 복제는 금지이고 `IsNonPublic` 호출만 허용한다.
   - 이 전용 dialer 위에서만 나가며 **우회용 기본 `http.Client`·직접 `net.Dial`은 린트로 금지**한다(예외 규칙은 §2.2).
   - (이전에 이견이던 'IP 분류 재사용 vs 도메인 정책 재사용 금지'는 이 결정으로 닫는다: 순수 함수 호출만 하고 정책·설정은 쓰지 않는다. 보안 재리뷰 조건.)
7. 동시성 1~2, 전역 속도 제한 **10회/분**(공개 한도 약 20회/분보다 낮게), 429/403 백오프, 결과 캐시(키에 사용자 정보 없음), 실행당 호출 상한(잡 ≤10, 대화 턴 ≤3).
8. 테스트: 사설·tailnet·loopback IP로 해석되면 거부, 공개 IP 1개+사설 IP 1개 혼합 해석이면 거부, 3xx 거부, 응답 크기 초과, 다른 호스트·http·포트 변경 불가, `tcp4`/`tcp6` 외 network·443 외 포트 거부. **프록시 env(`HTTP(S)_PROXY`, `ALL_PROXY`)가 설정된 상태에서도** 프록시를 거치지 않음. **IP 벡터 표**: IPv6 loopback·ULA·link-local, v4-mapped(`::ffff:127.0.0.1`, `::ffff:10.0.0.1`, `::ffff:100.64.0.1`), NAT64(`64:ff9b::7f00:1`, `64:ff9b:1::1`), 6to4(`2002:7f00:1::`), Teredo(`2001:0:…`), `100::1`, `2001:db8::1`, `192.0.2.1`, `198.51.100.1`, `203.0.113.1`, `192.88.99.1`, 그리고 공개 주소(허용 대조군).
9. **응답 처리 [Med-1]**: 엄격한 구조체로 파싱하고 `trackName`, `artistName`, `trackId`만 사용한다. `previewUrl`, `artworkUrl*`, `trackViewUrl`은 저장·전달하지 않는다. 카드의 링크는 응답 값이 아니라 **`trackId`(숫자)로 서버가 `https://music.apple.com/kr/...` 형태로 조립**한다(경로 형태는 PoC 확인). 응답 URL을 쓴다면 https·호스트 정확히 `music.apple.com`·userinfo/포트 없음을 검증하고 추적·제휴 파라미터를 제거하며 `music://`, `itms://`, `javascript:`, `data:`는 거부. 일치 판정은 NFKC·대소문자 정규화 후 **정확 일치**, 불일치 폐기.
10. **질의 최소화·유출 방지 [Med-2, Med-3]**: 질의에는 **아티스트명·제목(후보)만** 넣고 플레이리스트 이름·개인 메타·재생 통계는 넣지 않는다. 인자는 각 ≤64자, 제어문자·개행 거부, 툴 플랫폼 §7의 **'시크릿/tainted 텍스트 8자 이상 부분열 포함 시 차단'** 규칙을 이 툴에도 적용한다. **스케줄 잡의 프롬프트에는 대화·메모리를 넣지 않는다.** 모델에는 `match:bool`과 정규화된 (아티스트, 제목, id)만 돌려준다.
11. 외부 노출(Apple 검색·외부 검색엔진·iCloud)은 **승인됨 (건영 직접, 2026-10-28 09:46 KST)**(§0.1 #2a). `music.catalog.enabled`와 SearXNG 보조 질의 토글은 분리하고 기본 off. 외부 검색엔진 질의는 기존 SearXNG 경로를 쓰지만 `catalog_check`는 이 전용 클라이언트만 쓴다.

### 2.2 egress 단일 계층 규칙의 예외 [Med B-2]
`docs/tool-platform.md` §13의 'egress 단일 계층'(모든 외부 egress는 `internal/policy/domains` + SSRF Dialer로만 나감, 어댑터는 주입된 클라이언트만 사용, 직접 `net.Dial`·기본 클라이언트는 린트 금지) 규칙에는 **이 기능이 유일한 예외 1건**을 만든다. `catalog_check`는 단일 계층 밖의 전용 고정 클라이언트로 나간다(§2.1; 이유: 도메인 정책 denylist·와일드카드가 '정확히 한 호스트'를 보장하지 못함). 예외는 다음 조건으로만 성립한다.
1. **린트는 해당 패키지 경로 하나만 허용한다**(예: `internal/music/catalog`). 직접 `net/http` 클라이언트·`http.Transport`·`net.Dialer` 생성 허용 목록에 이 경로만 넣고, 다른 경로·와일드카드·하위 경로 확장은 허용하지 않는다. 허용 목록 변경은 MAJOR + 보안 리뷰.
2. **요청 직전에 활성화 여부를 확인한다.** 툴 노출 시점이 아니라 `Do()` 직전에 `GW_MUSIC_ENABLED`, `music.enabled`, `music.catalog.enabled`와 킬 스위치를 다시 읽고, 하나라도 off이거나 읽기 실패면 **네트워크 호출 없이 거부**(fail-closed). 카드 승인·잡 실행 사이에 설정이 꺼진 경우도 여기서 막힌다.
3. 공유 allowlist(`web-domains.yaml`, 매니페스트 `egress`)에 `itunes.apple.com`을 **추가하지 않는다.** 호스트 추가는 이 패키지 상수 변경(MAJOR)으로만 한다.
4. `tool-platform.md` §13에는 이 예외를 가리키는 **한 줄**만 둔다(본문은 이 문서가 원본).

## 3. 도구 동사와 결과 포맷
| 툴 | scope | verb | 확인 카드 | 비고 |
|---|---|---|---|---|
| `music.list_playlists()` | `music.read` | – | 없음 | 비스마트 사용자 플레이리스트의 `id,name,track_count,editable`만. 최대 100개 |
| `music.list_tracks(playlist_id, offset?, limit≤200)` | `music.read` | – | 없음 | managed 플레이리스트만. `truncated` 표시 |
| `music.search_library(query)` | `music.read` | – | 없음 | 라이브러리 안 검색(환각 방지 1차). 최대 20건 |
| `music.catalog_check(artist, title)` | `music.read` | – | 없음 | **외부 egress**(전용 고정 클라이언트 §2.1), `music.catalog.enabled=true`일 때만 노출. 최대 5건·gateway 캐시 |
| `music.playlist_add(playlist_id, track_id)` | `music.write` | `add` | **필수** | 라이브러리 곡만. 이미 있으면 `already_present`(멱등) |
| `music.playlist_remove(playlist_id, track_id)` | `music.write` | `delete` | **필수** | 목표: 플레이리스트 항목만 제거(라이브러리·파일 유지) — **실제 동작은 미검증(PoC에서 확인)**. 이미 없으면 `already_absent`. **#82 PoC(버려도 되는 라이브러리) 통과 전 구현·머지 금지**, 제거 전 스냅샷 필수(§7.1) |

- **scope [Med-6]**: `music.read`/`music.write`는 **API 키(Bearer)에 부여하지 않는다.** 재생 기록·라이브러리는 개인 정보이므로 메모리 툴처럼 **읽기도 `interactive`(세션) 한정**이다(#74/#67의 scope 모델과 일치). `music.owner_user_id` 한 명에게만 툴 노출.
- 쓰기는 기존 `pending_actions` 게이트: 모델은 `{"status":"pending_confirmation","action_id":...}`만 받고, 승인은 `interactive` scope(없으면 403 `scope_denied`). 자동 승인 없음. **헬퍼 verb는 툴과 1:1 5개**(`list_playlists`,`list_tracks`,`search_library`,`playlist_add`,`playlist_remove`). 플레이리스트 생성·삭제·이름 변경·재생 제어·메타 수정은 **없다**.
- 읽기 결과(untrusted 래핑, 곡·아티스트 문자열은 외부 입력으로 취급):
```json
{"status":"ok","data":{"playlist":{"id":"A1B2C3D4E5F60718","name":"드라이브","track_count":84},
 "tracks":[{"id":"0F1E2D3C4B5A6978","title":"…","artist":"…","album":"…","genre":"…","year":2019,
  "played_count":12,"played_at":"2026-03-02","skipped_count":3,"added_at":"2025-01-10","loved":false,"rating":0}],
 "truncated":false}}
```
- 날짜는 **일 단위 ISO 날짜**(시각 없음). 앨범아트·가사·파일 경로·위치 등은 반환하지 않는다.
- 쓰기 결과: `{"status":"ok","data":{"result":"added|removed|already_present|already_absent","playlist_id":"…","track_id":"…"}}`
- `catalog_check` 결과: `{"status":"ok","data":{"match":true,"title":"…","artist":"…","url":"https://music.apple.com/…"}}` (일치 없으면 `match:false`)
- 에러는 `error_code`만: `music_unavailable`, `permission_denied`(TCC), `not_managed`, `not_editable`, `not_in_library`, `kill_switch`, `timeout`, `rate_limited`.
- **확인 카드 `payload_view`**(보안 조건 승계): 실제 쓸 값 그대로 — 플레이리스트 이름, 곡 제목·아티스트, 그리고 제거 카드에는 **근거**(`played_count`, `played_at`, `added_at`, `skipped_count`)를 표시한다. 같은 제목·아티스트 곡이 여러 개일 수 있으므로 앨범·길이·추가일도 함께 보이고, **`payload_hash`는 id와 표시 값 둘 다에 바인딩**한다 **[Low-1]**. 모델의 '한 줄 이유'는 서버가 계산한 근거 수치와 시각적으로 구분하고 **'AI 생성'** 라벨을 붙인다 **[Med-4]**.
- **표시 보안 [Med-5]**: 곡·아티스트·앨범·플레이리스트 이름은 `textContent`로만 렌더링한다(HTML·마크다운·이미지·링크 해석 금지). 길이 상한을 두고 제어문자·**양방향(bidi) 제어문자**·제로폭 문자를 제거하거나 표시용으로 이스케이프한다(RTL 오버라이드로 카드 문구 위장 방지). 외부 이미지는 쓰지 않고 CSP `img-src 'self'`를 유지, 외부 링크는 `rel="noopener noreferrer"`와 `Referrer-Policy: no-referrer`, 링크는 `https://music.apple.com`만.

## 4. 추천 근거 (로컬 모델 + SearXNG로 충분한가)
**충분하다. 단, 모델은 "후보 생성기"일 뿐이고 "존재 검증"은 코드가 한다.**
1. **제거 제안 = 서버 규칙(모델 없음)**: managed 플레이리스트에서 `played_at`이 `music.stale_days`(기본 180일)보다 오래됐거나 재생 이력이 없고, `added_at`이 `music.min_added_days`(기본 90일)보다 오래됐으며, `loved`가 아니고 `rating<80`인 곡. 한 번에 최대 `music.max_suggestions`(기본 10)건. 모델은 한 줄 설명 문구만 만든다.
2. **통계 신뢰도 게이트**: 재생 정보 없는 곡 비율이 높거나(예: 80%↑, 임계값은 PoC로 확정) 동기화 이상이 의심되면 **제거 제안을 하지 않고** "재생 통계가 불완전해요"만 알린다(iPhone 재생이 Mac에 반영되지 않는 사례 보고 있음).
3. **추가 제안 1순위 = 라이브러리 우선**: 라이브러리에 있지만 그 플레이리스트에 없는 곡을 서버가 장르·아티스트 겹침과 재생 통계로 순위화(모델 없이도 가능), 모델은 상위 후보 재정렬·이유 문구만. → **환각 0**(실존 곡만).
4. **추가 제안 2순위 = 카탈로그 후보(옵션, 기본 off)**: 모델이 (아티스트,제목) 후보 ≤10개 제안 → gateway가 `catalog_check`(iTunes Search API, `country=KR`)로 **정규화 후 정확 일치**만 통과, 미통과는 버린다(건수만 기록). 통과 곡은 **`music.apple.com` 링크 카드로만** 보여 준다. 건영이 직접 라이브러리에 담으면(동기화 후) 다음 실행에서 `search_library`로 찾아 "플레이리스트에 추가" 카드로 승격한다. 자동 라이브러리 추가는 안 한다.
5. **프롬프트 입력**: 곡 원문 전체가 아니라 **집계 메타**(장르 분포, 상위 아티스트·연대, 재생수 분포)와 샘플 ≤50곡만 넣는다. SearXNG는 실행당 ≤3회 "유사 아티스트/신보" 질의로 보조하며(**별도 토글**, 기본 off, 질의에는 아티스트명·장르만, 플레이리스트 이름·개인 메타·재생 통계 금지; 외부 검색엔진 질의는 **승인됨 (건영 직접, 2026-10-28 09:46 KST)**) 결과는 untrusted 래핑, 근거로만 쓰고 후보 존재 증명으로는 쓰지 않는다. 스케줄 잡 프롬프트에는 대화·메모리를 넣지 않는다.
6. **한계**: 소형 로컬 모델은 곡 지식이 얕고 환각이 많다 → 검증 게이트로 걸러지므로 **제안 수가 줄 뿐 오답이 보이지는 않는다**(통과율은 PoC에서 측정, 목표치는 이슈 4 수용 기준). iTunes Store 카탈로그와 Apple Music 스트리밍 카탈로그가 100% 같지는 않다(링크가 안 열릴 수 있음). Search API는 약 20회/분 제한과 403 가능 → 캐시·백오프. 취향은 재생 통계 기반이라 새 취향 반영이 느리다. Apple의 API 이용 약관(프로모션 콘텐츠 용도)에 맞게 **메타데이터와 링크만** 쓰고 미리듣기·제휴 링크는 쓰지 않는다 → 보안/법무 확인.

## 5. 설정 키 (`/v1/settings` allowlist 추가, 모두 기본 off·끄기 우선)
- `music.enabled` bool **false**(툴 노출). **env 전용(settings로 변경 불가) [Med-8]**: `GW_MUSIC_ENABLED`(기본 false), `GW_MUSIC_SOCKET_PATH`, `GW_MUSIC_OWNER_USER_ID`(세션이 게이트웨이를 임의 소켓으로 향하게 하지 못하도록; 청사진=사용자별 매핑).
- 아래 `music.*` 키만 `/v1/settings` allowlist(interactive 한정). 수치는 서버가 하한·상한으로 clamp한다.
- `music.managed_playlists` string[] **[]**(최대 20, persistent id). 켜기 화면에서 선택, 비면 `music.enabled`를 true로 저장할 수 없음. **저장 시 헬퍼 `list_playlists` 결과와 대조해 존재·비스마트·편집 가능한 id만 받고**, 변경은 interactive 한정이며 `tool_audit` 기록 대상 **[Med-8]**
- `music.writes_enabled` bool **false**(운영 플래그 + 사용자 설정 둘 다 true여야 쓰기 verb 노출)
- `music.catalog.enabled` bool **false**(`catalog_check` egress + 카탈로그 후보; 허용 도메인은 `itunes.apple.com`만). **켜기 화면에서 미리 체크하지 않는다** — 무엇이 나가는지(§0.1 #2a 표) 문구를 읽고 건영이 직접 켠다 **[Med-2]**. 외부 노출은 **승인됨 (건영 직접, 2026-10-28 09:46 KST)**
- `music.recommend_search.enabled` bool **false**(SearXNG 보조 질의, `catalog`와 **별도 토글**) **[Med-2]**
- `music.digest.enabled` bool **false**, `music.digest.frequency` "weekly"(기본, 또는 "daily"), `music.digest.weekday` "sun", `music.digest.time` "10:00", `music.digest.tz`(기본 Asia/Seoul)
- `music.history_retention_days` int **0**(= 자동 삭제 off, 기본). 켤 때의 30일 설정은 **미승인·보류**. **자동 삭제는 건영 별도 승인 전까지 구현·활성화하지 않는다**(이슈 #83, §10 미승인)
- `music.stale_days` 180, `music.min_added_days` 90, `music.max_suggestions` 10, `music.suggestion_ttl_days` 14, `music.dismiss_cooldown_days` 90
- 변경은 `interactive` 전용 라우트. **새 라우트 전부를 `TestRouteAuthMatrix`에 추가**하며 매트릭스는 패키지 전체 등록 라우트 집합과 비교하는 형태여야 한다(예상 라우트: 제안 위젯 조회, 곡 '넣기/빼기' 탭(카드 생성), 거절, 켜기 화면의 플레이리스트 목록, `PATCH /v1/settings`의 `music.*`; 모두 세션 전용·Bearer 403·쿠키는 CSRF 보호). 하나라도 off면 off(`GW_MUSIC_ENABLED`, `music.enabled`, 잡 `enabled`, `GW_PROACTIVE_ENABLED`).

### 5.1 게이트 계층 — 어느 계층이 무엇을 막는가 [Low: music.enabled]
`music.enabled`는 한 곳의 `if`가 아니라 아래 네 계층에서 **각각** 막는다. 어느 하나가 off이거나 값을 읽지 못하면 그 계층에서 거부(fail-closed)한다. 하위 스위치(`catalog.enabled`, `writes_enabled`, `recommend_search.enabled`, `digest.enabled`)는 `music.enabled`가 off이면 값과 무관하게 off로 취급한다(반대 방향 종속은 없음).

| 계층 | 입력 | off일 때 막는 것 |
|---|---|---|
| 1. 환경(프로세스 시작) | `GW_MUSIC_ENABLED`(env 전용) | `music.*` 툴 **미등록**, `music.*` 라우트 미등록(404), `music_digest` 템플릿 시드·실행 안 함, 헬퍼 소켓에 연결하지 않음. settings로 되살릴 수 없다 |
| 2. 설정(사용자 토글, 모델에 보이는 면) | `music.enabled`(settings, interactive 한정) | 모델에 `music.*` 툴 비노출(owner가 아닌 사용자 포함), 잡 실행 skip, 위젯 조회·'넣기/빼기' 탭 라우트 403 `kill_switch`. 이미 만들어진 `pending_actions` 카드도 승인 시점에 다시 확인해 403 |
| 3. 실행 직전(툴 핸들러·헬퍼 클라이언트·`catalog_check`) | 위 값들을 호출마다 다시 읽음 | 헬퍼 호출·외부 HTTP 요청을 **네트워크·소켓 접근 전에** 거부(설정이 승인 중에 꺼진 경우 포함, §2.2-2) |
| 4. 헬퍼(host) | 킬 스위치 파일·peer 검사 | 게이트웨이 설정과 **독립으로** 동작: 킬 스위치가 있으면 읽기 전용으로 강등. 헬퍼가 떠 있는 것만으로는 아무 툴도 열리지 않는다(1~3계층이 게이트) |

테스트: 각 계층을 하나씩 off로 두고 (툴 목록·라우트·잡·카드 승인·`catalog_check` 요청·헬퍼 호출)이 모두 거부되는 표 테스트, 하위 스위치가 on이어도 `music.enabled` off면 off인지 검사.

## 6. DB 스키마 변경: 새 테이블 **1개**
```sql
music_suggestions (
  id            TEXT PRIMARY KEY,
  user_id       <users.id> NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  kind          TEXT NOT NULL CHECK (kind IN ('remove','add_library','add_catalog_link')),
  playlist_ref  TEXT NOT NULL,          -- Music persistent id
  track_ref     TEXT NOT NULL,          -- persistent id, 카탈로그는 sha256(정규화 artist|title)
  catalog       JSONB NULL,             -- add_catalog_link 전용: {title, artist, url} (공개 카탈로그 정보, 제목 기록 승인됨)
  state         TEXT NOT NULL CHECK (state IN ('pending','confirming','applied','dismissed','expired','failed')),
  created_at    TIMESTAMPTZ NOT NULL, decided_at TIMESTAMPTZ NULL, expires_at TIMESTAMPTZ NOT NULL,
  UNIQUE (user_id, kind, playlist_ref, track_ref)
)
```
- 목적: 같은 제안 반복 방지(거절 쿨다운)와 TTL 만료. 감사는 기존 `tool_audit`(id·개수만).
- **소유·경쟁 조건 [Med-7]**: 모든 조회·전이는 `user_id` 소유 확인(타 사용자는 404). 상태 전이 `pending → confirming → applied/failed`는 **원자적 조건부 UPDATE**로 처리해 이중 탭·재전송이 같은 제안을 두 번 실행하지 못하게 한다. `payload_hash`에는 `user_id`·액션 종류·`playlist_id`·`track_id`(+표시 값, Low-1)를 포함하고 120초 토큰은 1회 소비. 위젯 '넣기/빼기' 탭(카드 생성)과 시트 승인은 **서로 다른 요청**이며 한 번의 탭이 승인을 겸하는 단축은 불허한다.
- **곡명 보관 [Med-11]** — **승인됨 (건영 직접, 2026-10-28 09:46 KST)**: 위젯 카드가 스레드 메시지로 저장되어 곡명·표시 수치가 대화 기록에 남는 것은 허용되었다. 곡명·재생 통계는 스레드에 평문으로 남고, 자동 삭제 미승인이라 **기본 보존**이다. 카탈로그 후보의 제목·아티스트·링크도 저장 가능. 이 보존 범위를 설정 화면과 켜기 화면에 명시한다. 이번 승인은 **대화 기록 보관만** 허용했고 메모리 추출·피드백 내보내기는 허용하지 않았다.
- **메모리 추출·내보내기 제외는 메시지·툴 결과 단위 표시로 한다 [Med B-3]** (스레드 단위 제외는 폐기: 음악과 무관한 대화까지 막고, 음악 내용이 다른 스레드로 인용되면 못 막기 때문). 표시 규칙:
  1. **표시 값**: `messages.metadata`(JSONB, `db-model.md`)에 `data_class:"music"` 한 키를 쓴다. 판정과 기록은 **게이트웨이가 쓰기 시점에** 하며 모델이 정하지 않는다.
  2. **표시 대상(원천)**: (a) `music.*` 툴 호출의 결과 메시지, (b) 제안 위젯 카드·확인 카드 메시지, (c) `music_digest` 잡이 만든 모든 메시지.
  3. **표시 전파(파생)**: 어시스턴트 메시지는 그 메시지를 만든 턴의 모델 컨텍스트에 **표시된 메시지가 하나라도 있었으면** 표시한다(그 결과를 인용했는지 의미를 판정하지 않고 보수적으로 일괄). 이후 턴은 표시된 메시지가 컨텍스트 윈도우에 남아 있는 동안만 전파되고, 윈도우에서 빠지면 전파가 끝난다. 사용자가 입력한 메시지는 표시하지 않는다.
  4. **효과**: 표시된 메시지는 (a) 메모리 추출·`memory.propose` 입력, (b) `/v1/feedback/export` 등 내보내기에서 **제외**한다. 스레드 화면과 사용자 본인의 대화 검색에는 그대로 보인다(대화 기록 보관은 승인됨). 표시되지 않은 같은 스레드의 일반 대화는 정상 처리된다.
  5. **사용자가 직접 입력한 텍스트**(예: "이 곡 좋아해 기억해 둬")는 표시 대상이 아니므로 메모리 제안이 가능하다. 단 그 제안이 표시된 메시지의 내용을 인용·복사한 것이면 거부한다(제안 본문과 표시 메시지 부분열 일치 검사).
  6. **fail-closed**: 표시 기록이 실패하면 해당 메시지 저장을 거부하고, 컨텍스트 구성 중 표시 여부를 알 수 없는 메시지는 표시된 것으로 취급한다.
  7. **테스트**: music 툴 결과·카드·잡 메시지가 추출·export에서 빠짐, 같은 스레드의 일반 메시지는 포함됨, 전파(표시 메시지가 컨텍스트에 있던 턴의 어시스턴트 답)와 전파 종료, 표시 기록 실패 시 저장 거부, 모델이 표시를 끄거나 켤 방법이 없음.
- `scheduled_jobs.template_id='music_digest'`(새 템플릿), `last_status`에 `skipped_empty`는 M1에서 추가됨 → 재사용. 보존: 만료·거절 행은 **기본 자동 삭제 없음**(`music.history_retention_days=0`; 해시/opaque id뿐). 자동 삭제(켤 때 30일 설정 포함)는 **미승인·보류** — 건영 별도 승인 후에만 일 1회 정리 잡을 만든다(삭제 건수만 `tool_audit`, 내용 없음).

## 7. 잡 · 확인 카드 흐름
1. **잡**(`music_digest`, 기본 off, 기본 주 1회·`daily` 선택 가능): ① `list_tracks`(managed만, 고정 수집) ② 규칙으로 제거 후보 계산 ③ 라이브러리 우선 추가 후보 ④ (옵션) 모델 후보→`catalog_check` ⑤ 모델은 **쓰기 툴 없이** 이유 문구만 ⑥ 결과를 `music_suggestions`(pending)로 저장하고 스레드 "음악 제안"에 **위젯 카드 한 장**(두 구역: **넣을 곡 목록 / 뺄 곡 목록**, 곡마다 근거와 항목별 버튼)으로 전달한다. 헬퍼 무응답 시 "음악 확인 불가"만 표시. 비어 있으면 `skipped_empty`.
2. 잡(스케줄 실행은 시작부터 tainted)은 **쓰기를 실행하지 않고 새 job도 만들지 않는다.** 제안 행 저장은 서버 코드가 하며 모델은 행을 만들 수 없다.
3. **승인(곡마다)**: 건영이 위젯의 한 곡에서 "넣기"/"빼기"를 누르면(`interactive` scope) 서버가 **그 곡 하나**로 `pending_actions` 확인 카드를 만든다(`payload_hash` 바인딩, 120초 1회용 토큰, 실제 값·근거 표시). 승인 시 헬퍼가 id로 다시 읽어 상태를 확인하고(트랙이 아직 플레이리스트에 있나, 재생 이력이 그새 바뀌지 않았나) **1건씩** 실행한다. **전체 일괄 승인 버튼 없음**(건영 결정 #1: 곡마다 확인한 것만 실행). 확인하지 않은 곡은 TTL 만료 시 `expired`가 되며 아무것도 바뀌지 않는다.
4. 거절 → `dismissed`(쿨다운). 만료 → `expired`. 실패 → `failed` + error_code. 재시도는 사용자가 다시 누를 때만(헬퍼 verb가 멱등이라 중복 위험은 낮음).
5. 대화 중 직접 요청("이 곡 빼 줘")도 같은 `music.playlist_*` 툴과 카드를 쓴다. **카드 남발 상한 [Med-4]**: 서버가 **턴당 pending 카드 1개, 사용자당 동시 pending ≤3, 일일 쓰기 ≤30건**을 강제하고, 곡 메타를 읽은 턴은 tainted로 표시한다. 상한 도달은 `rate_limited`로 처리하고 `tool_audit`에 기록한다.

### 7.1 제거(`playlist_remove`) 안전 조건 [High-2] (쓰기 verb 구현 전 필수)
AppleScript `delete`는 참조가 어디서 해석되느냐에 따라 플레이리스트 항목만 지우기도, 라이브러리 항목(과 파일)까지 지우기도 한다. 근거 일부가 검색 요약이고 macOS 26 동작은 미확인이므로 다음을 지킨다.
1. **#82 PoC를 버려도 되는 라이브러리(별도 임시 라이브러리·계정)에서 실측해 `delete`가 플레이리스트 항목만 지움을 확인하기 전에는 `playlist_remove` 구현·머지 금지.**
2. 헬퍼가 매번 대상이 user playlist이고 `special kind`가 none, 스마트 아님, 폴더 아님, 편집 가능, managed 목록 포함임을 재확인한다. 라이브러리(Library) 플레이리스트에는 어떤 경우에도 `delete`를 보내지 않는다.
3. 트랙 참조는 **해당 플레이리스트의 요소로서** 해석한다(라이브러리 전체에서 찾은 참조를 `delete`에 넘기지 않음). 같은 곡이 중복 항목이면 모호성으로 거부하거나 위치까지 명시한다.
4. 제거 직전 **전체 목록 스냅샷(플레이리스트 id·track id·위치)**을 로컬에 저장한다(**쓰기 활성화 전 필수**, '되돌리기 청사진'에서 격상). id만 담아 0600으로 보관하고 곡명은 로그에 남기지 않는다.
5. 단계적 롤아웃: `writes` 켤 때 **`add`만 먼저**, `remove`는 PoC·스냅샷 구현 후 별도로 켠다.

## 8. 리스크
| 리스크 | 대응 |
|---|---|
| 곡 제거 오탐(통계 부정확) | 규칙 임계값, 신뢰도 게이트, 항목별 카드에 근거 표시, 제거 대상은 플레이리스트 항목만(라이브러리 곡이 남는지는 미검증(PoC에서 확인), §7.1) |
| 프롬프트 인젝션(곡명·SearXNG 결과) | 입력 untrusted 래핑, 잡에 쓰기 툴 없음, 제거 후보는 서버 규칙, 카드에 실제 값 표시 |
| TCC·GUI 세션 의존 | 건영 직접 승인, 고정 서명 ID, 세션 없으면 `music_unavailable` |
| macOS 업데이트로 스크립트 동작 변경(26 회귀 사례) | 현재 쓰는 verb만 PoC로 검증, 버전 기록, 실패 시 읽기 전용 강등 |
| 쓰기가 iCloud 동기화로 외부 전파 | 별도 승인, `music.writes_enabled` 이중 off 스위치 |
| 외부 egress 신설(`itunes.apple.com`) | 기본 off, 전용 고정 클라이언트(§2.1: 상수 호스트·경로, 리다이렉트 거부, timeout·크기 상한, 사설 IP·rebinding 차단), 캐시, 호출 상한, 보안 리뷰. 노출(IP·질의 문자열)은 **승인됨 (건영 직접, 2026-10-28 09:46 KST)** |
| `delete` 대상 오지정으로 라이브러리 항목 삭제 | §7.1: 버리는 라이브러리 PoC 전 구현 금지, 종류 재확인, 스냅샷 선행 |
| 헬퍼가 Music.app을 실행하지 않으므로 앱이 꺼져 있으면 제안·쓰기 불가 | `music_unavailable` 표시, 설치 안내에 'Music 앱을 켜 둔다' 명시(보안 조건) |
| 곡명 DB 보관 → 메모리 추출 | 대화 기록 보관은 **승인됨 (건영 직접, 2026-10-28 09:46 KST)**. music 툴 결과·카드·그 파생 어시스턴트 메시지는 **메시지 단위 표시**로 메모리 추출·내보내기 제외(§6), 자동 삭제 미승인 → 기본 보존 |
| 되돌리기 어려움 | **쓰기 활성화 전 스냅샷 필수**(§7.1). UI의 '되돌리기'(라이브러리에 남아 있으면 `duplicate`로 재추가; 위치는 끝)는 후속 청사진 |
| 다중 사용자 | `music.owner_user_id` 한 명만 툴 노출(M1과 동일) |

## 9. SemVer 1차 판단: **MAJOR** (켜기는 각각 별도 MAJOR 릴리스)
- 새 host 프로세스 신뢰 경계(gateway↔`eve-music` 소켓), 새 TCC Automation 권한, 쓰기의 외부 동기화, 새 egress 도메인이 있어 보안 경계 변경이다. 불확실하면 MAJOR 원칙이고 보안 담당도 MAJOR 유지에 동의했다.
- **PM 결정(2026-10-28)·보안 담당 의견에 따라 `music`, `writes`, `catalog`는 각각 별도 MAJOR 릴리스로 켠다.** 코드 배포는 전부 기본 off이고, 활성화마다 별도 배포·건영 직접 승인이다. 이 PR(문서) 머지는 어느 활성화의 승인도 아니다.

| 릴리스 | 켜는 것 | 내용 | 비고 |
|---|---|---|---|
| MAJOR A | `music.enabled` | `eve-music` 설치(한 줄 설치)·TCC·읽기 verb·제안 위젯(읽기만) | 헬퍼 신뢰 경계·개인 데이터 접근 |
| MAJOR B | `music.writes_enabled` | `playlist_add` 먼저. `playlist_remove`는 #82 PoC 통과·스냅샷 구현 후 별도 활성화 | iCloud 동기화는 **승인됨 (건영 직접, 2026-10-28 09:46 KST)** |
| MAJOR C | `music.catalog.enabled`(+SearXNG 보조 질의는 별도 토글) | `catalog_check` 전용 클라이언트·`itunes.apple.com` egress | Apple 검색·외부 검색엔진 검색은 **승인됨 (건영 직접, 2026-10-28 09:46 KST)** |

- 순서(A → B → C)는 권장이며 각 릴리스는 서로 독립이다. 한 릴리스를 켜도 다른 것은 켜지지 않는다.
- 선행: M1(캘린더 어댑터 `eve-pim`, #68~#73) 완료 → `EveHelperKit` 추출(별도 PR) → 이 기능. M1에서 이미 승인된 항목(리마인더·캘린더 쓰기, 본인 한정 Web Push·이메일, 브리핑 자동 삭제)은 **이 기능으로 승계되지 않는다.**

## 10. 승인 표 ([건영 승인 필요] 항목 현황, 2026-10-28 09:46 KST 기준)
| 항목 | 상태 | 비고 |
|---|---|---|
| 곡 제거(데이터 삭제: 플레이리스트 항목) — 곡마다 확인 카드 | 곡마다 확인 방식: **승인됨 (건영 직접, 2026-10-28 08:39 KST)**. iCloud 동기화: **승인됨 (건영 직접, 2026-10-28 09:46 KST)** | 활성화(`music.writes_enabled`)는 별도 MAJOR 릴리스, `playlist_remove`는 #82 PoC 후 |
| 곡 추가(iCloud 동기화로 외부 전파) — 곡마다 확인 카드 | 곡마다 확인 방식: **승인됨 (건영 직접, 2026-10-28 08:39 KST)**. iCloud 동기화: **승인됨 (건영 직접, 2026-10-28 09:46 KST)** | M1 캘린더 쓰기 승인과 별개 항목으로 기록 |
| 추천용 검색: Apple 검색(`catalog_check`, `itunes.apple.com` 전용 클라이언트)과 외부 검색엔진 검색(기존 SearXNG 경로) | **승인됨 (건영 직접, 2026-10-28 09:46 KST)** (원문: "Apple 검색 허용, 외부 검색엔진 검색 허용"). 이전 PM 결정 '외부 호출은 `itunes.apple.com`만'은 이 승인으로 대체 | `catalog_check`는 보안 High-1에 따라 계속 전용 고정 클라이언트. 켜기는 별도 MAJOR(`catalog`), 토글 분리·사전 체크 금지 |
| 노래 제목 기록(스레드·대화 기록에 곡명 보관) | **승인됨 (건영 직접, 2026-10-28 09:46 KST)** (원문: "노래 제목 기록 허용") | 메시지·툴 결과 단위 표시로 메모리 추출·피드백 내보내기 제외 유지(§6), 자동 삭제는 미승인이라 기본 보존 |
| 도우미 설치(캘린더 어댑터와 같은 한 줄 설치) | **PM 결정 (2026-10-28 09:39 KST, 건영 승인 대상 아님)** | TCC 팝업은 건영이 host에서 직접 승인(승인 대상이라기보다 설치 중 직접 조작) |
| 관리 대상 플레이리스트(켤 때 선택) | **PM 결정 (2026-10-28 09:39 KST, 건영 승인 대상 아님)** | 선택 행위는 건영 직접 |
| 지난 추천 기록 자동 삭제(기본 off, 켤 때 30일 설정) | **미승인 · 보류** (#83과 함께) | 30일 설정값도 보류 |
| 자동 삭제 실행(데이터 삭제) 승인 | **미승인** [건영 승인 필요] | 별도 승인 전 구현·활성화 금지 |
| Web Push·이메일 전달 | **미승인**(기본 인앱뿐) | M1 §10.2 승인은 아침 브리핑 알림 범위, 이 기능으로 승계 안 됨 |
| 유료 개발자 계정/MusicKit 비용 | **미승인**(불필요) | 청사진만 |

## 11. 보안 담당 리뷰 필요
1. `eve-music` 경계: 소켓·그룹 재사용(`_evepim`) — **보안 담당 답변: 공유 허용**(실질 분리는 별 소켓·바이너리·TCC·킬 스위치); peer uid 검사·스키마·8 KiB·네트워크 없음(샌드박스 강제)·로그 규칙.
2. AppleScript 고정 템플릿+인자 전달 방식의 주입 안전성, 헬퍼에서 `play`·파일 접근·메타 수정 부재 확인.
3. 스케줄 실행(tainted)이 서버 코드로 `music_suggestions`를 만들고 사용자 탭이 `pending_actions`를 만드는 흐름이 [S4-T] 규칙과 맞는지.
4. `itunes.apple.com` egress: 전용 고정 클라이언트(§2.1), 특히 IP 분류 로직을 순수 함수로 공유해도 되는지(PM '정책 재사용 금지'와의 조정), Apple 이용 약관 준수.
5. 곡·아티스트·SearXNG 문자열의 untrusted 처리와 카드 `payload_view` 정확성.
6. TCC 승인 절차·고정 서명·재서명 시 권한 복구 절차.
7. 새 scope(`music.read`/`music.write`)와 `interactive` 전용 라우트의 `TestRouteAuthMatrix` 포함.
8. SemVer 판정(MAJOR 유지 여부).
9. 위젯 항목 탭 → 서버가 곡 1건의 `pending_actions` 카드 생성 → 확인 시트(실제 값 표시)에서 `interactive`로 승인하는 2단계가 "곡마다 확인" 요건과 확인 게이트 규칙에 충분한지(한 번의 탭으로 승인을 겸하는 단축은 허용하지 않는 안으로 설계).
10. 설치 절차를 캘린더 어댑터와 합칠 때 두 헬퍼의 권한 분리(별 바이너리·별 TCC·별 킬 스위치) 유지 확인.

## 12. 확장 청사진
- `MusicSource` 어댑터 인터페이스: `MusicApp`(채택) | `AppleMusicAPI`(유료 계정 후; 카탈로그 검색·라이브러리 추가·API 생성 플레이리스트 편집). 툴·카드·`music_suggestions`는 소스에 무관하다. **`AppleMusicAPI` 구현은 별도 보안 리뷰(MAJOR)가 필요**하며 이 설계에 토큰 필드를 미리 만들지 않는다. 그때 조건: `.p8` 개인키 0600(가능하면 Keychain, repo·로그·env 인자 금지), 개발자 토큰(JWT)은 짧은 수명으로 게이트웨이가 발급, Music User Token은 암호화 저장·브라우저/모델 비노출, 키 로테이션·폐기 절차. 유료 Program은 비용이라 건영 승인 사항 **[Low-2]**. 이 설계에는 새 시크릿이 없다(iTunes Search는 키 없음) — 유지한다.
- 사용자별 소스: `music_sources(user_id, kind, ref)` 테이블(다중 사용자 시).
- 플레이리스트 생성("Eve 추천" 새 플레이리스트에만 쓰기)·되돌리기·Apple 개인정보 포털 재생 기록 가져오기(수동 가져오기, 통계 보강)는 후속 MINOR.
- 인증 방식이 바뀌어도 이 기능은 principal·scope만 보므로 영향 없다.

## 13. 보안 리뷰 반영 추적표 (PR #81 리뷰, 검토 head `a83835a`)
반영 위치는 이 문서의 절, 관련 이슈는 eve-serving 이슈 번호다(#82 PoC, #84 헬퍼, #85 게이트웨이 툴·설정, #86 제안 엔진·DB, #87 쓰기, #88 잡·UI, #89 카탈로그 egress, #83 자동 삭제 보류).

| 항목 | 요지 | 반영 위치 | 관련 이슈 |
|---|---|---|---|
| High-1 | `catalog_check` 전용 고정 클라이언트(상수 호스트·경로, 리다이렉트 거부, timeout·크기 상한, 사설 IP·rebinding 차단, 도메인 정책 재사용 금지) | §2, §2.1 | #89 |
| High-2 | `playlist_remove`는 버리는 라이브러리 PoC 전 구현 금지, 쓰기 켜기 전 스냅샷 | §3, §7.1, §8, §9 | #82, #87 |
| Med-1 | iTunes 응답 엄격 구조체, 링크는 `trackId`로 서버 조립 | §2.1-9 | #89 |
| Med-2 | 질의는 아티스트·제목만, `catalog.enabled` 사전 체크 금지, SearXNG 보조 질의 별도 토글 | §2.1-10, §4, §5 | #89, #85, #88 |
| Med-3 | 모델 질의 유출 방지(길이·문자 제한, tainted 부분열 차단, 잡 프롬프트에 대화·메모리 금지) | §2.1-10, §4 | #89, #86 |
| Med-4 | 카드 남발 상한(턴당 1·동시 3·일 30), 모델 문구 'AI 생성' 라벨 | §3, §7-5 | #87, #88 |
| Med-5 | 곡 메타 `textContent`·bidi 제거·CSP·`noreferrer`, 외부 링크는 시스템 브라우저 | §2(UI/iOS), §3 | #88 |
| Med-6 | `music.read/write`는 API 키(Bearer)에 부여 안 함, 읽기도 interactive 한정 | §3 | #85 |
| Med-7 | 제안 소유 확인·원자적 상태 전이·`payload_hash` 범위·탭과 승인은 별도 요청 | §6 | #86, #87 |
| Med-8 | `socket_path`·`owner_user_id` env 전용, `managed_playlists` 검증, 수치 clamp | §5 | #85 |
| Med-9 | '네트워크 없음'을 샌드박스로 강제, 인프로세스 AppleScript, 허용 단어 테스트 | §2(헬퍼 강화) | #84 |
| Med-10 | root 소유 설치 경로, Music.app 자동 실행 방지, 호출 타임아웃·동시성 1 | §2(헬퍼 강화), §8 | #84 |
| Med-11 | 곡명 스레드 보관·메모리 추출 불일치 해소 | §6 | #86, #88 |
| Med B-3 (재리뷰 977f1f6) | 메모리 추출·내보내기 제외를 스레드 단위에서 메시지·툴 결과 단위 표시로 변경, 표시 규칙 | §6 | #86, #88 |
| Med B-2 (재리뷰) | tool-platform §13 egress 단일 계층의 예외 명시, 린트는 해당 패키지 경로만 허용, 요청 직전 활성화 확인 | §2.2, `docs/tool-platform.md` §13 예외 한 줄 | #89 |
| High-1 보강 (재리뷰) | Control 판정(원본+`Unmap()`), tcp4/tcp6·443, `DialContext` 고정, `Proxy: nil`+프록시 env 테스트, IP 벡터, '전용 복사' 삭제, 추가 거부 대역 | §2.1-4, §2.1-6, §2.1-8 | #89 |
| Low (재리뷰) | 승인 출처 표기 통일, 미검증 `delete` 표기, `music.enabled` 게이트 계층, PoC iCloud 동기화 끄기 필수 | §0.1, §0·§1·§3·§8, §5.1, `scripts/poc/music-probe.md` | #82, #85 |
| (조건 14) | 새 라우트 전부 `TestRouteAuthMatrix`(패키지 전체 집합 비교) | §5 | #85, #87, #88 |
| Low-1 | 중복 곡 구분 표시, 해시에 표시값 포함 | §3 | #87, #88 |
| Low-2 | `AppleMusicAPI` 청사진은 별도 MAJOR 리뷰, 토큰 필드 미리 만들지 않음 | §12 | – |
| Low-3 | `EveHelperKit` 추출 별도 PR, 의존성 고정 | §2(헬퍼 강화) | #84 |
| Low-4 | 킬 스위치 독립성·읽기 전용 강등 테스트 | §2(헬퍼 강화) | #84, #87 |
| Low-5 | 내부 채팅 메시지 id 제거 | §0.1(문구 정리) | – |
| PM-(1)~(4) | 전용 클라이언트 / remove 보류·스냅샷 / Med 이슈 분배 / music·writes·catalog 각각 별도 MAJOR | §2.1, §7.1, 이 표, §9 | #89, #87, #84~#89, 전체 |
| 승인 반영 | 건영 09:46 KST: Apple 검색·외부 검색엔진 검색·iCloud 동기화·노래 제목 기록 허용 | §0.1, §6, §10 | #85, #86, #88, #89 |

### 이견·확인 항목
1. ~~IP 분류 재사용~~ **해결(재리뷰 977f1f6)**: `IsNonPublic` 순수 함수를 호출하고 복사본·도메인 정책 재사용은 하지 않는다(§2.1-6).
2. **설치 경로의 관리자 권한**: root 소유 경로(Med-10)는 한 줄 설치가 관리자 권한을 요구할 수 있다. eve-pim 설치 규칙과의 일치 여부는 #84에서 확정한다.
3. **Music 앱이 꺼져 있으면 사용 불가**: 헬퍼가 앱을 실행하지 않는다는 보안 조건의 사용성 비용이다(§8).
4. **스레드 곡명과 메모리 추출**: 건영은 대화 기록 보관만 승인했다. 메모리 추출·내보내기 제외는 보안 권고로 유지하고 메시지·툴 결과 단위 표시(§6)로 구현한다. 이 제외를 풀려면 건영에게 따로 확인받는다.
5. **웹 API 관련 주장 일부는 검색 요약에만 근거**(Apple 개발자 포럼 403). 문서에 미검증으로 표시했다.
6. **`IsNonPublic`에 추가 대역을 넣을지**(§2.1-6): 지금은 클라이언트 패키지의 추가 목록으로 처리. 기존 정책 패키지 변경은 별도 PR·보안 리뷰.
