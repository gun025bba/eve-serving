# iOS 셸 설계 (Eve Shell)

상태: **보안 조건부 승인(2026-10-27)**. 머지 전 최종 head 보안 리뷰 필요. 사용자 결정: 무료 Apple ID + SideStore(또는 AltStore+AltServer) 사이드로딩을 먼저 하고, 푸시가 필요해지면 $99 TestFlight로 옮긴다.

## 1. 구조
- **얇은 네이티브 셸**: SwiftUI 앱 하나에 `WKWebView` 하나. 시작 URL은 `https://<TS_HOST>/ui/` 고정(빌드 설정값).
- 화면·로직은 모두 eve-chat-ui(PWA)에 있다. **FE 배포가 즉시 반영**되고 셸은 다시 설치할 필요가 없다. 셸은 네이티브만 할 수 있는 일만 맡는다.
- tailnet 밖이거나 Tailscale이 꺼져 있으면 셸 내장 오프라인 화면("Tailscale 연결 확인")을 보여 준다. 외부 URL로 대체하지 않는다.

## 2. 책임 경계
| iOS 셸 | FE(eve-chat-ui) |
|---|---|
| 상태 표시줄 스타일, 상단 블러 제거, safe area(`contentInsetAdjustmentBehavior = .never` + CSS `env(safe-area-inset-*)` 전달) | 레이아웃, CSS safe area 적용 |
| 키보드 위 자동완성/액세서리 바 제거(`inputAccessoryView` nil 처리한 WKWebView 서브클래스) | 입력창 동작, `autocomplete`/`autocorrect` 속성 |
| 외부 링크를 Safari로 열기, 내비게이션 제한 | 모든 앱 기능·인증 화면(페어링) |
| 나중에: 푸시, Siri, 위젯 | 브리지 기능 감지와 대체 동작 |

## 3. JS 브리지
- 채널: `window.webkit.messageHandlers.eve.postMessage(msg)`(웹→셸), `window.__eveShell.receive(msg)`(셸→웹, `evaluateJavaScript`).
- 메시지 형식: `{ "v": 1, "id": "<uuid>", "type": "<name>", "payload": {...} }`. 응답은 `{ "v":1, "re":"<id>", "ok":true|false, "error_code"?: "..." }`.
- **핸드셰이크**: 페이지 로드 후 웹이 `hello {bridge: "1.0.0"}`를 보낸다. 셸은 `capabilities {bridge: "1.0.0", shell: "<셸 semver>", features: ["haptics", ...]}`로 답한다. 메이저가 다르면 웹은 브리지를 쓰지 않는다(일반 PWA로 동작). 마이너 차이는 `features`로만 판단한다.
- **모르는 메시지**: 셸은 `ok:false, error_code:"unknown_type"`으로 답하고 무시한다. 웹도 모르는 셸 메시지는 무시한다. 둘 다 예외를 던지지 않는다.
- **출처 제한(보안 조건)**: 셸은 `WKScriptMessage.frameInfo.isMainFrame == true`이고 `securityOrigin`의 scheme·host·port가 설정된 출처(`https`, `<TS_HOST>`, 443)와 **각각 정확히 같을 때만** 메시지를 처리한다(접두어·부분 문자열 비교 금지). 그 밖의 메시지는 버린다.
- **내비게이션**: `decidePolicyFor`는 설정된 출처가 아닌 모든 이동을 거부한다(사용자가 탭한 외부 https 링크만 Safari로 넘김). `file://`, `data:`, `javascript:` 스킴은 차단하고, 새 창 생성(`createWebViewWith`)은 항상 nil을 반환한다. ATS 예외는 두지 않는다.
- 브리지는 **비밀·토큰을 주고받지 않는다**(쿠키·세션 정보 전달 메시지 없음).

## 4. 인증 (identity-transport.md와 일치)
- 셸도 브라우저와 똑같이 **페어링 → 세션 쿠키**를 쓴다. `WKWebsiteDataStore.default()`(영구 저장소)에 HttpOnly 쿠키로 저장된다.
- 네이티브 저장소(Keychain/UserDefaults)에는 토큰을 두지 않는다. 셸은 Bearer를 보내지 않는다. 셸은 브라우저 세션이므로 `/v1/memory*`, approve 같은 세션 전용 규칙이 그대로 적용된다.
- 주의: Safari PWA와 셸은 쿠키 저장소가 달라서 **기기마다 셸에서도 따로 페어링**해야 한다. WKWebView 영구 저장소에도 ITP·저장소 정리 정책이 적용된다. 유휴 기간이 길거나 앱을 다시 설치하거나 다른 Apple ID로 다시 서명하면 쿠키가 사라질 수 있고, 그때는 재페어링한다(세션 수명 유휴 30일/절대 180일은 서버 기준).
- **페어링 해제(필수)**: 셸 설정에 'unpair' 동작을 둔다. `WKWebsiteDataStore.default().removeData(ofTypes: allWebsiteDataTypes, modifiedSince: .distantPast)`로 쿠키와 웹사이트 데이터를 모두 지운다. 서버 세션 revoke는 웹의 로그아웃 API로 먼저 시도한다.
- 나중에 푸시를 넣으면 APNs 기기 토큰은 비밀이 아닌 식별자로 다룬다. 세션 쿠키로 인증한 요청으로 등록한다.

## 5. 셸 배포와 버전
- **배포**: SideStore/AltStore source JSON(`apps[].versions[]`: version, date, downloadURL, size, sha256). JSON과 서명되지 않은 IPA는 tailnet 안 정적 경로(예: `https://<TS_HOST>/shell/source.json`)나 비공개 저장소 릴리스에 둔다. 공개 인터넷에는 올리지 않는다.
- **보안 조건**: SideStore가 쿠키 없이 source를 가져오지 못하면 URL 쿼리 토큰이나 새 비인증 경로를 만들지 **않는다**. 이때는 IPA를 수동으로 설치한다(파일 앱/AirDrop으로 SideStore에 열기). 이런 우회책이 필요하면 별도 보안 검토를 받는다.
- **서명**: 빌드 산출물은 서명하지 않은 IPA다. 기기의 SideStore가 사용자 무료 Apple ID로 서명하고 7일마다 갱신한다. 저장소에는 인증서·프로비저닝을 두지 않는다.
- **버전**: 셸 semver는 gateway·웹 버전과 **별개**다(`shell-vX.Y.Z` 태그). 메이저는 브리지 메이저 변경이나 번들 ID 변경, 마이너는 브리지 기능 추가, 패치는 버그 수정. 웹은 `capabilities`로 셸 버전을 확인하고, 오래된 셸에서도 동작해야 한다.
- 번들 ID는 고정한다. 바꾸면 App ID 한도를 쓰고 쿠키·데이터도 잃는다.

## 6. 무료 계정 제약
- 프로비저닝 7일 만료 → SideStore 백그라운드 갱신 필요. 만료되면 앱이 안 열린다. [Apple][SideStore]
- 기기당 앱 3개(SideStore 포함) [Apple][SideStore]
- App ID 7일당 10개(위젯·확장도 각자 App ID를 쓴다) [Apple][cert.fish]
- 기기 3대 등록 [Apple]
- 푸시(APNs), App Groups, Associated Domains, Siri 등 다수 capability 없음 → 푸시·Siri, 앱과 데이터를 공유하는 위젯은 불가 [Apple capability 표][cert.fish]

출처: https://developer.apple.com/help/account/basics/about-your-developer-account/ · https://developer.apple.com/help/account/reference/supported-capabilities-ios/ · https://docs.sidestore.io/docs/faq · https://cert.fish/guide/methods/free-apple-account

## 7. 확장 청사진 (TestFlight, $99)
- 같은 번들 ID로 ADP 팀에 옮기면 TestFlight 90일 빌드가 되고 7일 재서명이 없어진다. 처음부터 번들 ID와 코드 구조를 그대로 옮길 수 있게 둔다.
- 푸시: `register_push {token}` 브리지 메시지 + gateway `POST /v1/devices`(세션 인증)를 추가한다. 이 API는 그때 별도 계약으로 정하고 외부 발송(APNs)이라 사용자 승인 대상이다.
- Siri(App Intents)와 위젯: App Groups가 필요하다. 표시용 최소 데이터만 공유 컨테이너에 두고 세션 쿠키는 공유하지 않는다.
- 하드코딩 지점: 시작 URL, 허용 출처 하나. 나중에 목록 설정으로 바꾼다.

## 8. SemVer
- 저장소/gateway: **변경 없음**(셸은 새 클라이언트이고 서버 계약을 바꾸지 않는다). 서버 쪽 추가(예: `/shell/` 정적 서빙)가 필요해지면 **MINOR**다.
- 셸: `shell-v0.1.0`부터 시작한다.
- 보안 경계: 새 인증 경로는 없다(같은 세션 쿠키). 다만 브리지가 웹 콘텐츠에 네이티브 기능을 노출하므로, 브리지에 민감한 기능(파일, 알림, 외부 발송)을 추가할 때마다 보안 검토가 필요하다. 푸시 도입은 MAJOR 후보로 다시 판단한다.

## 9. 보안 검토 결과
조건부 승인: 출처 정확 일치·내비게이션 제한(§3), source 비인증 경로 금지·수동 설치 대체(§5), 영구 데이터 저장소 허용 + unpair 필수(§4). 머지 전 최종 head 리뷰.
