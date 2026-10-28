# 음악 플레이리스트 관리 — 설계 요약 — **제안 · 미구현**

상태: 설계 초안(시스템 아키텍트), **건영 결정 반영(2026-10-28 08:39 KST)**. 착수 시점: **M1(캘린더 연동·아침 브리핑) 이후**.

> **경계 원칙(인증 독립)**: 기능은 인증 방식에 의존하지 않는다. 툴·라우트는 미들웨어가 붙인 principal `{user_id, scopes}`(내부 `users.id`)만 보고 필요한 scope만 검사한다. 쿠키·세션·페어링·Bearer 구분은 인증 계층 안에만 둔다.

## 0. 결론 (5줄)
1. **$99 없이 가능한 길은 (a) host의 Music.app 자동화뿐이다.** 재생 횟수·마지막 재생일·건너뜀 읽기, 플레이리스트에 곡 추가(`duplicate`)·제거(`delete`, 라이브러리 곡은 안 지워짐)가 모두 된다.
2. **(b) MusicKit/Apple Music API는 무료 계정으로 불가**(개발자 토큰·Media ID 모두 유료 Program 필요). 유료여도 웹 API는 재생수 미제공·플레이리스트 곡 삭제 미지원이라 이 기능에는 맞지 않다 → 청사진으로만 남긴다.
3. **카탈로그(라이브러리에 없는) 곡의 자동 추가는 불가**(공식 스크립트 명령 없음, UI 스크립팅은 취약). → "라이브러리 우선 추천" + "카탈로그 곡은 Apple Music 링크만 제안, 건영이 직접 라이브러리에 담으면 그다음 추가 카드"로 설계한다.
4. **제거 제안은 서버 규칙(재생일·추가일·별점)으로 결정하고, 모델은 추천 이유와 새 곡 후보만 만든다.** 후보는 라이브러리/카탈로그 검증을 통과해야 보여 주므로 곡 이름 환각이 사용자에게 닿지 않는다. 로컬 모델 + SearXNG로 충분하되 "검증 게이트"가 핵심이다.
5. **구조는 캘린더 어댑터(eve-pim) 패턴 그대로**: 별도 LaunchAgent 헬퍼 `eve-music` + 0660 소켓, 읽기 verb / 확인 카드 쓰기 verb, 주간 잡, 인앱 스레드. SemVer **MAJOR**(새 host 헬퍼·TCC Automation·쓰기 동기화). 쓰기는 **자동 실행 없음**: 목록 카드에서 건영이 곡마다 확인한 것만 실행한다. 모든 기능은 기본 off.

## 0.1 건영 결정과 승인 상태 (2026-10-28 08:39 KST 반영)
출처 확인: PM 채팅 기록에서 건영 본인 메시지(t118u, 08:39 KST)로 확인한 것, PM이 결정한 것(09:39 KST), 아직 승인이 없는 것을 구분한다. 같은 시각 PM이 띄운 선택 카드(허용 항목 6개)에는 **건영이 카드로 답하지 않고 채팅으로 답했다**(카드 선택 없음).

| # | 결정 | 상태 | 근거 |
|---|---|---|---|
| 1 | 곡 추가·제거를 자동으로 하지 않는다. 주기적으로(하루 1회든 주 1회든) **넣을 곡 목록·뺄 곡 목록을 위젯 카드로** 보여 주고, 건영이 **곡마다 확인한 것만** 실행한다 | **승인됨 (건영 직접, 2026-10-28 08:39 KST)** | 건영 메시지 t118u("…넣을 곡 리스트, 뺄 곡 리스트를 … 위젯 카드 형식으로 … 컨펌을 하면 그때 넣고 빼는 식으로"). 주기 기본값 주 1회는 PM 선택 |
| 2 | 추천을 위한 검색 허용. 외부 호출은 **`itunes.apple.com`만** 허용 | 검색 허용: **승인됨 (건영 직접, 2026-10-28 08:39 KST)**. 도메인 한정: **PM 결정 (2026-10-28 09:39 KST, 건영 승인 대상 아님)** | t118u("추천을 위해서는 … 검색이 가능해야겠지"). 도메인 한정은 보안 범위를 좁히는 PM 결정이며 egress 정책 변경은 보안 리뷰 대상 |
| 3 | 도우미(`eve-music`)는 캘린더 어댑터와 **같은 한 줄 설치**에 넣는다 | **PM 결정 (2026-10-28 09:39 KST, 건영 승인 대상 아님)** | 설치 중 TCC 팝업은 건영이 host에서 직접 눌러야 하는 점은 그대로 |
| 4 | 관리할 플레이리스트는 **기능을 켤 때 고른다** | **PM 결정 (2026-10-28 09:39 KST, 건영 승인 대상 아님)** | 고르는 행위(선택)는 건영이 직접 한다 |
| 5 | 지난 추천 기록 **자동 삭제는 기본 off**, 켤 때 30일 설정 | **미승인 · 보류**([건영 승인 필요], 이슈 #83과 함께) | 30일 설정값도 승인 전까지 확정하지 않는다. 건영 본인 메시지에 없음 |
| 6 | 자동 삭제의 **삭제 승인은 나중에 따로** 받는다 | **미승인으로 남김** [건영 승인 필요] | 승인 전에는 삭제 잡을 구현·활성화하지 않는다 |
| 7 | 착수는 M1 다음 | 건영 요청 맥락과 일치(PM 전달) | – |

> 위 5·6은 데이터 삭제 규칙상 건영 직접 승인이 필요하며 승인 전까지 구현·활성화하지 않는다. 항목 3·4와 도메인 한정은 PM 결정이라 건영 승인 대상이 아니다.

## 1. 방식 비교 (무료 가능 여부)
| 항목 | (a) Music.app 자동화 (AppleScript/JXA) | (b) MusicKit for Swift / Apple Music API / MusicKit JS |
|---|---|---|
| 비용·계정 | **무료**, 개발자 계정 불필요 | **유료 Program 필수**. MusicKit App Service는 App ID 설정(유료)이고, 개발자 토큰(ES256 JWT)은 Media ID+개인키+Team ID가 필요. 무료 Personal Team은 불가 |
| 플레이리스트 읽기 | `user playlist`, `tracks` 가능 | 웹 API `GET /v1/me/library/playlists` 가능 |
| played count / played date / skipped | **가능**(`played count`, `played date`, `skipped count`, `skipped date`) | Swift `Song.playCount/lastPlayedDate`는 있으나(앱·유료 전제), **웹 API는 미제공** |
| 플레이리스트에 곡 추가 | 라이브러리 곡만 `duplicate … to user playlist`. 모든 사용자 플레이리스트 가능 | `POST …/tracks`는 `canEdit` 플레이리스트만(앱이 만든 것 위주) |
| 플레이리스트에서 제거 | **가능**(`delete track of user playlist`, 플레이리스트 항목만 삭제). 스마트/동적 플레이리스트는 불가 | **불가**(DELETE/PUT 403 보고) |
| 카탈로그 곡 추가 | **직접 불가**(라이브러리에 먼저 있어야 함). `open location`+System Events UI 클릭은 가능하나 취약 → 채택 안 함 | 카탈로그 검색·`POST /v1/me/library`로 추가 가능(토큰 전제) |
| 카탈로그 검증(환각 방지) | 해당 없음 → **iTunes Search API**(무료, 키 없음, 약 20회/분)로 별도 검증 | 카탈로그 검색 API |
| eve-ios 사용 | 불필요(host에서 처리) | eve-ios는 무료 Personal Team/SideStore → **MusicKit 불가** |
| 운영 제약 | **로그인된 GUI 세션의 LaunchAgent** + TCC Automation 1회 승인(건영 직접) | 해당 없음 |
| 평가 | **채택** | **M1 이후 범위에서 제외**, `MusicSource` 어댑터 청사진만 |

### 근거 URL (직접 열람 / 검색 요약 구분)
- Music.app 속성·플레이리스트 동작(검색 요약): https://github.com/sundial-org/awesome-openclaw-skills/blob/main/skills/apple-music-2/SKILL.md , `delete`는 플레이리스트 항목만 삭제: https://dougscripts.com/itunes/pdf/removeduplicates.pdf
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
- **새 컴포넌트 1개**: host의 `eve-music`(Swift, **건영 로그인 세션 LaunchAgent**). 캘린더 어댑터와 **같은 규칙**: 소켓 `<user-run-dir>/music.sock` 0660, 그룹 `_evepim`(사람·에이전트 없음, `_evegw`만), 디렉터리 0750 고정 소유자, 기동 시 기존 파일·심볼릭 링크 `lstat` 거부, `getpeereid`로 peer uid=`_evegw` 정확 일치, 고정 스키마·**요청 8 KiB 상한**·모르는 필드/메서드 거부, **네트워크 없음**, **로그에 곡·플레이리스트 이름 금지**(개수·id만).
- 왜 eve-pim과 **별 프로세스**인가: TCC 권한이 바이너리별(Automation→Music vs 캘린더)이고, 킬 스위치와 폭발 반경을 분리하기 위해서다. 소켓 서버·peer 검사·스키마 검증은 공용 Swift 패키지(`EveHelperKit`)로 공유한다(코드 중복 방지).
- **AppleScript 주입 방지**: 헬퍼는 **고정 스크립트 템플릿**만 실행하고 값은 문자열 결합이 아니라 인자(`on run argv`)로 넘긴다. id는 `^[0-9A-F]{16}$`, 검색어는 길이 ≤64로 검증한다. 스크립트에 `play` 계열 동작은 없다.
- **설치**: 캘린더 어댑터와 **같은 한 줄 설치**에 넣는다(두 LaunchAgent 설치, 권한 팝업 두 개를 건영이 한 자리에서 처리). 기능 켜기 전에는 헬퍼가 떠 있어도 툴은 노출되지 않는다.
- **TCC**: Automation(Music) 팝업은 GUI 세션에서 건영이 직접 1회 승인한다. 바이너리를 다시 서명하면 재승인이 필요할 수 있으므로 **고정 서명 ID**로 배포한다(런북에 `tccutil` 복구 절차). 로그아웃 상태면 헬퍼가 없으므로 `music_unavailable`로 처리한다(M1과 동일).
- **gateway**: 기존 툴 레지스트리·`pending_actions`·`scheduled_jobs`·인앱 선톡 전달 재사용. 카탈로그 검증(`music.catalog_check`)은 **헬퍼가 아니라 gateway**의 단일 egress 계층(도메인 정책)에서 `itunes.apple.com`(정확 일치)만 허용한다. **기본 off.**
- **UI/iOS**: 웹 UI의 스레드·카드로만 표시. eve-ios는 변경 없음(푸시 없음, 인앱 전달만). 카탈로그 링크는 `music.apple.com` URL이며 외부 앱으로 여는 처리는 프론트가 확인한다.
- **관리 대상은 opt-in, 켤 때 고른다**: 기능 켜기 화면이 `music.list_playlists`(이름·개수만) 목록을 보여 주고 건영이 고른 것(최대 20)이 `music.managed_playlists`(persistent id)가 된다. **1개 이상 고르지 않으면 켜지지 않는다.** 그 플레이리스트만 Eve가 곡을 읽고 바꾼다.

## 3. 도구 동사와 결과 포맷
| 툴 | scope | verb | 확인 카드 | 비고 |
|---|---|---|---|---|
| `music.list_playlists()` | `music.read` | – | 없음 | 비스마트 사용자 플레이리스트의 `id,name,track_count,editable`만. 최대 100개 |
| `music.list_tracks(playlist_id, offset?, limit≤200)` | `music.read` | – | 없음 | managed 플레이리스트만. `truncated` 표시 |
| `music.search_library(query)` | `music.read` | – | 없음 | 라이브러리 안 검색(환각 방지 1차). 최대 20건 |
| `music.catalog_check(artist, title)` | `music.read` | – | 없음 | **외부 egress**, `music.catalog.enabled=true`일 때만 노출. 최대 5건·gateway 캐시 |
| `music.playlist_add(playlist_id, track_id)` | `music.write` | `add` | **필수** | 라이브러리 곡만. 이미 있으면 `already_present`(멱등) |
| `music.playlist_remove(playlist_id, track_id)` | `music.write` | `delete` | **필수** | 플레이리스트 항목만 제거(라이브러리·파일 유지). 이미 없으면 `already_absent` |

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
- **확인 카드 `payload_view`**(보안 조건 승계): 실제 쓸 값 그대로 — 플레이리스트 이름, 곡 제목·아티스트, 그리고 제거 카드에는 **근거**(`played_count`, `played_at`, `added_at`, `skipped_count`)를 표시한다.

## 4. 추천 근거 (로컬 모델 + SearXNG로 충분한가)
**충분하다. 단, 모델은 "후보 생성기"일 뿐이고 "존재 검증"은 코드가 한다.**
1. **제거 제안 = 서버 규칙(모델 없음)**: managed 플레이리스트에서 `played_at`이 `music.stale_days`(기본 180일)보다 오래됐거나 재생 이력이 없고, `added_at`이 `music.min_added_days`(기본 90일)보다 오래됐으며, `loved`가 아니고 `rating<80`인 곡. 한 번에 최대 `music.max_suggestions`(기본 10)건. 모델은 한 줄 설명 문구만 만든다.
2. **통계 신뢰도 게이트**: 재생 정보 없는 곡 비율이 높거나(예: 80%↑, 임계값은 PoC로 확정) 동기화 이상이 의심되면 **제거 제안을 하지 않고** "재생 통계가 불완전해요"만 알린다(iPhone 재생이 Mac에 반영되지 않는 사례 보고 있음).
3. **추가 제안 1순위 = 라이브러리 우선**: 라이브러리에 있지만 그 플레이리스트에 없는 곡을 서버가 장르·아티스트 겹침과 재생 통계로 순위화(모델 없이도 가능), 모델은 상위 후보 재정렬·이유 문구만. → **환각 0**(실존 곡만).
4. **추가 제안 2순위 = 카탈로그 후보(옵션, 기본 off)**: 모델이 (아티스트,제목) 후보 ≤10개 제안 → gateway가 `catalog_check`(iTunes Search API, `country=KR`)로 **정규화 후 정확 일치**만 통과, 미통과는 버린다(건수만 기록). 통과 곡은 **`music.apple.com` 링크 카드로만** 보여 준다. 건영이 직접 라이브러리에 담으면(동기화 후) 다음 실행에서 `search_library`로 찾아 "플레이리스트에 추가" 카드로 승격한다. 자동 라이브러리 추가는 안 한다.
5. **프롬프트 입력**: 곡 원문 전체가 아니라 **집계 메타**(장르 분포, 상위 아티스트·연대, 재생수 분포)와 샘플 ≤50곡만 넣는다. SearXNG는 실행당 ≤3회 "유사 아티스트/신보" 질의로 보조하며 결과는 untrusted 래핑, 근거로만 쓰고 후보 존재 증명으로는 쓰지 않는다.
6. **한계**: 소형 로컬 모델은 곡 지식이 얕고 환각이 많다 → 검증 게이트로 걸러지므로 **제안 수가 줄 뿐 오답이 보이지는 않는다**(통과율은 PoC에서 측정, 목표치는 이슈 4 수용 기준). iTunes Store 카탈로그와 Apple Music 스트리밍 카탈로그가 100% 같지는 않다(링크가 안 열릴 수 있음). Search API는 약 20회/분 제한과 403 가능 → 캐시·백오프. 취향은 재생 통계 기반이라 새 취향 반영이 느리다. Apple의 API 이용 약관(프로모션 콘텐츠 용도)에 맞게 **메타데이터와 링크만** 쓰고 미리듣기·제휴 링크는 쓰지 않는다 → 보안/법무 확인.

## 5. 설정 키 (`/v1/settings` allowlist 추가, 모두 기본 off·끄기 우선)
- `music.enabled` bool **false**(툴 노출). 서버: `GW_MUSIC_ENABLED`(기본 false), `music.socket_path`, `music.owner_user_id`(하드코딩 대신 설정; 청사진=사용자별 매핑).
- `music.managed_playlists` string[] **[]**(최대 20, persistent id). 켜기 화면에서 선택, 비면 `music.enabled`를 true로 저장할 수 없음
- `music.writes_enabled` bool **false**(운영 플래그 + 사용자 설정 둘 다 true여야 쓰기 verb 노출)
- `music.catalog.enabled` bool **false**(`catalog_check` egress + 카탈로그 후보; 허용 도메인은 `itunes.apple.com`만). 건영이 추천용 검색을 허용했으므로 켜기 화면에서 이 항목을 미리 체크해 보여 주되 값은 건영이 저장해야 한다
- `music.digest.enabled` bool **false**, `music.digest.frequency` "weekly"(기본, 또는 "daily"), `music.digest.weekday` "sun", `music.digest.time` "10:00", `music.digest.tz`(기본 Asia/Seoul)
- `music.history_retention_days` int **0**(= 자동 삭제 off, 기본). 켤 때의 30일 설정은 **미승인·보류**. **자동 삭제는 건영 별도 승인 전까지 구현·활성화하지 않는다**(이슈 #83, §10 미승인)
- `music.stale_days` 180, `music.min_added_days` 90, `music.max_suggestions` 10, `music.suggestion_ttl_days` 14, `music.dismiss_cooldown_days` 90
- 변경은 `interactive` 전용 라우트(`TestRouteAuthMatrix`에 추가). 하나라도 off면 off(`GW_MUSIC_ENABLED`, `music.enabled`, 잡 `enabled`, `GW_PROACTIVE_ENABLED`).

## 6. DB 스키마 변경: 새 테이블 **1개**
```sql
music_suggestions (
  id            TEXT PRIMARY KEY,
  user_id       <users.id> NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  kind          TEXT NOT NULL CHECK (kind IN ('remove','add_library','add_catalog_link')),
  playlist_ref  TEXT NOT NULL,          -- Music persistent id
  track_ref     TEXT NOT NULL,          -- persistent id, 카탈로그는 sha256(정규화 artist|title)
  catalog       JSONB NULL,             -- add_catalog_link 전용: {title, artist, url} (공개 카탈로그 정보)
  state         TEXT NOT NULL CHECK (state IN ('pending','applied','dismissed','expired','failed')),
  created_at    TIMESTAMPTZ NOT NULL, decided_at TIMESTAMPTZ NULL, expires_at TIMESTAMPTZ NOT NULL,
  UNIQUE (user_id, kind, playlist_ref, track_ref)
)
```
- 목적: 같은 제안 반복 방지(거절 쿨다운)와 TTL 만료. **개인 곡명은 저장하지 않는다**(라이브러리 곡은 표시 시 헬퍼에서 다시 읽음). 감사는 기존 `tool_audit`(id·개수만).
- `scheduled_jobs.template_id='music_digest'`(새 템플릿), `last_status`에 `skipped_empty`는 M1에서 추가됨 → 재사용. 보존: 만료·거절 행은 **기본 자동 삭제 없음**(`music.history_retention_days=0`; 해시/opaque id뿐). 자동 삭제(켤 때 30일 설정 포함)는 **미승인·보류** — 건영 별도 승인 후에만 일 1회 정리 잡을 만든다(삭제 건수만 `tool_audit`, 내용 없음).

## 7. 잡 · 확인 카드 흐름
1. **잡**(`music_digest`, 기본 off, 기본 주 1회·`daily` 선택 가능): ① `list_tracks`(managed만, 고정 수집) ② 규칙으로 제거 후보 계산 ③ 라이브러리 우선 추가 후보 ④ (옵션) 모델 후보→`catalog_check` ⑤ 모델은 **쓰기 툴 없이** 이유 문구만 ⑥ 결과를 `music_suggestions`(pending)로 저장하고 스레드 "음악 제안"에 **위젯 카드 한 장**(두 구역: **넣을 곡 목록 / 뺄 곡 목록**, 곡마다 근거와 항목별 버튼)으로 전달한다. 헬퍼 무응답 시 "음악 확인 불가"만 표시. 비어 있으면 `skipped_empty`.
2. 잡(스케줄 실행은 시작부터 tainted)은 **쓰기를 실행하지 않고 새 job도 만들지 않는다.** 제안 행 저장은 서버 코드가 하며 모델은 행을 만들 수 없다.
3. **승인(곡마다)**: 건영이 위젯의 한 곡에서 "넣기"/"빼기"를 누르면(`interactive` scope) 서버가 **그 곡 하나**로 `pending_actions` 확인 카드를 만든다(`payload_hash` 바인딩, 120초 1회용 토큰, 실제 값·근거 표시). 승인 시 헬퍼가 id로 다시 읽어 상태를 확인하고(트랙이 아직 플레이리스트에 있나, 재생 이력이 그새 바뀌지 않았나) **1건씩** 실행한다. **전체 일괄 승인 버튼 없음**(건영 결정 #1: 곡마다 확인한 것만 실행). 확인하지 않은 곡은 TTL 만료 시 `expired`가 되며 아무것도 바뀌지 않는다.
4. 거절 → `dismissed`(쿨다운). 만료 → `expired`. 실패 → `failed` + error_code. 재시도는 사용자가 다시 누를 때만(헬퍼 verb가 멱등이라 중복 위험은 낮음).
5. 대화 중 직접 요청("이 곡 빼 줘")도 같은 `music.playlist_*` 툴과 카드를 쓴다.

## 8. 리스크
| 리스크 | 대응 |
|---|---|
| 곡 제거 오탐(통계 부정확) | 규칙 임계값, 신뢰도 게이트, 항목별 카드에 근거 표시, 라이브러리 곡은 남음(플레이리스트 항목만 제거) |
| 프롬프트 인젝션(곡명·SearXNG 결과) | 입력 untrusted 래핑, 잡에 쓰기 툴 없음, 제거 후보는 서버 규칙, 카드에 실제 값 표시 |
| TCC·GUI 세션 의존 | 건영 직접 승인, 고정 서명 ID, 세션 없으면 `music_unavailable` |
| macOS 업데이트로 스크립트 동작 변경(26 회귀 사례) | 현재 쓰는 verb만 PoC로 검증, 버전 기록, 실패 시 읽기 전용 강등 |
| 쓰기가 iCloud 동기화로 외부 전파 | 별도 승인, `music.writes_enabled` 이중 off 스위치 |
| 외부 egress 신설(`itunes.apple.com`) | 기본 off, 도메인 정확 일치, 쿼리에 사용자 식별자 없음, 캐시, 보안 리뷰 |
| 되돌리기 어려움 | 제거 카드에 스냅샷(플레이리스트 id·track id·위치)을 남겨 "되돌리기"(라이브러리에 남아 있으면 `duplicate`로 재추가; 위치는 끝)를 후속 청사진으로 둔다 |
| 다중 사용자 | `music.owner_user_id` 한 명만 툴 노출(M1과 동일) |

## 9. SemVer 1차 판단: **MAJOR**
- 새 host 프로세스 신뢰 경계(gateway↔`eve-music` 소켓), 새 TCC Automation 권한, 쓰기의 외부 동기화, (옵션) 새 egress 도메인이 있어 보안 경계 변경으로 본다. 확실하지 않으면 MAJOR 원칙. 보안 담당 판정 후 하향 여부 재검토(전부 기본 off지만 경계는 바뀜).
- 순서: M1(캘린더 어댑터 `eve-pim`, #68~#73) 완료 → `EveHelperKit` 추출 → 이 기능. M1에서 이미 승인된 항목(리마인더·캘린더 쓰기, 본인 한정 Web Push·이메일, 브리핑 자동 삭제)은 **이 기능으로 승계되지 않는다.**

## 10. 승인 표 ([건영 승인 필요] 항목 현황, 2026-10-28 08:39 KST 기준)
| 항목 | 상태 | 비고 |
|---|---|---|
| 곡 제거(데이터 삭제: 플레이리스트 항목) — 곡마다 확인 카드 | **승인됨 (건영 직접, 2026-10-28 08:39 KST)** — 곡마다 확인한 것만 실행 | 건영 t118u. 실제 활성화(`music.writes_enabled`)는 배포 시 보고 |
| 곡 추가(iCloud 동기화로 외부 전파) — 곡마다 확인 카드 | **승인됨 (건영 직접, 2026-10-28 08:39 KST)** — 곡마다 확인한 것만 실행 | 건영 t118u. M1 캘린더 쓰기 승인과 별개 항목으로 기록 |
| 추천용 검색(SearXNG, 카탈로그 확인 외부 호출) | 검색 허용: **승인됨 (건영 직접, 2026-10-28 08:39 KST)**. 외부 호출 도메인은 `itunes.apple.com`만: **PM 결정 (2026-10-28 09:39 KST, 건영 승인 대상 아님)** | egress 도메인은 보안 리뷰 필수 |
| 도우미 설치(캘린더 어댑터와 같은 한 줄 설치) | **PM 결정 (2026-10-28 09:39 KST, 건영 승인 대상 아님)** | TCC 팝업은 건영이 host에서 직접 승인(승인 대상이라기보다 설치 중 직접 조작) |
| 관리 대상 플레이리스트(켤 때 선택) | **PM 결정 (2026-10-28 09:39 KST, 건영 승인 대상 아님)** | 선택 행위는 건영 직접 |
| 지난 추천 기록 자동 삭제(기본 off, 켤 때 30일 설정) | **미승인 · 보류** (#83과 함께) | 30일 설정값도 보류 |
| 자동 삭제 실행(데이터 삭제) 승인 | **미승인** [건영 승인 필요] | 별도 승인 전 구현·활성화 금지 |
| Web Push·이메일 전달 | **미승인**(기본 인앱뿐) | M1 §10.2 승인은 아침 브리핑 알림 범위, 이 기능으로 승계 안 됨 |
| 유료 개발자 계정/MusicKit 비용 | **미승인**(불필요) | 청사진만 |

## 11. 보안 담당 리뷰 필요
1. `eve-music` 경계: 소켓·그룹 재사용(`_evepim`)이 적절한지, 별 그룹이 필요한지; peer uid 검사·스키마·8 KiB·네트워크 없음·로그 규칙.
2. AppleScript 고정 템플릿+인자 전달 방식의 주입 안전성, 헬퍼에서 `play`·파일 접근·메타 수정 부재 확인.
3. 스케줄 실행(tainted)이 서버 코드로 `music_suggestions`를 만들고 사용자 탭이 `pending_actions`를 만드는 흐름이 [S4-T] 규칙과 맞는지.
4. `itunes.apple.com` egress(정확 일치, rebinding·리다이렉트 차단, 쿼리 내용), Apple 이용 약관 준수.
5. 곡·아티스트·SearXNG 문자열의 untrusted 처리와 카드 `payload_view` 정확성.
6. TCC 승인 절차·고정 서명·재서명 시 권한 복구 절차.
7. 새 scope(`music.read`/`music.write`)와 `interactive` 전용 라우트의 `TestRouteAuthMatrix` 포함.
8. SemVer 판정(MAJOR 유지 여부).
9. 위젯 항목 탭 → 서버가 곡 1건의 `pending_actions` 카드 생성 → 확인 시트(실제 값 표시)에서 `interactive`로 승인하는 2단계가 "곡마다 확인" 요건과 확인 게이트 규칙에 충분한지(한 번의 탭으로 승인을 겸하는 단축은 허용하지 않는 안으로 설계).
10. 설치 절차를 캘린더 어댑터와 합칠 때 두 헬퍼의 권한 분리(별 바이너리·별 TCC·별 킬 스위치) 유지 확인.

## 12. 확장 청사진
- `MusicSource` 어댑터 인터페이스: `MusicApp`(채택) | `AppleMusicAPI`(유료 계정 후; 카탈로그 검색·라이브러리 추가·API 생성 플레이리스트 편집). 툴·카드·`music_suggestions`는 소스에 무관하다.
- 사용자별 소스: `music_sources(user_id, kind, ref)` 테이블(다중 사용자 시).
- 플레이리스트 생성("Eve 추천" 새 플레이리스트에만 쓰기)·되돌리기·Apple 개인정보 포털 재생 기록 가져오기(수동 가져오기, 통계 보강)는 후속 MINOR.
- 인증 방식이 바뀌어도 이 기능은 principal·scope만 보므로 영향 없다.
