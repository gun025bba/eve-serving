# 보안: 브라우저 쓰기 CSRF 방어

게이트웨이는 상태를 바꾸는 요청(POST, PUT, PATCH, DELETE)에 아래 검사를 모두 적용한다. 구현은 `internal/handlers/csrf.go`.

| 검사 | 규칙 | 실패 reason |
|---|---|---|
| 허용 목록 | `GW_ALLOWED_ORIGINS`가 비어 있으면 모든 브라우저 쓰기 거부(fail closed) | `no_allowlist` |
| 헤더 | `X-Eve-Request: 1` 필수 | `missing_header` |
| Origin | 없거나 `null`이면 거부 | `missing_origin` |
| Origin | 허용 목록 항목과 **정확히** 일치(접두어·끝 슬래시 불가) | `origin_mismatch` |
| Sec-Fetch-Site | 값이 있으면 `same-origin`이어야 함, 없으면 통과 | `fetch_site` |
| Content-Type | 본문이 있으면 `application/json`(파라미터 허용). 본문 없는 POST/DELETE는 통과 | `content_type` |

- 실패 시 `403 {"error_code":"invalid"}`. 이유는 로그 `csrf_rejected`의 `reason` 필드와 메트릭 `eve_csrf_rejected_total{reason}`에 남는다.
- 예외: API 키(`Authorization: Bearer`)로 인증된 요청만. Tailscale identity·쿠키 등 브라우저 인증은 예외가 아니다.
- GET/HEAD/OPTIONS는 검사하지 않는다.

## 설정

```
GW_ALLOWED_ORIGINS=https://gateway.example.test   # 게이트웨이 자신의 공개 출처. 쉼표로 여러 개
```

호스트 이름은 저장소에 넣지 않는다. 실제 값은 배포 환경(launchd plist 등)에만 둔다.
UI가 켜져 있는데 값이 비어 있으면 기동 시 `csrf_allowlist_empty` 경고를 남기고 모든 브라우저 쓰기를 거부한다.

## UI 호환성

UI는 모든 쓰기 요청에 `X-Eve-Request: 1`과 `Content-Type: application/json`을 보내야 한다. 브라우저는 같은 출처 POST/PUT/PATCH/DELETE에 Origin을 자동으로 붙인다.
eve-chat-ui v1.0.1은 `X-Eve-Request`를 보내지 않으므로, 이 변경은 이 헤더를 보내는 UI 버전이 배포된 뒤에 배포해야 한다.

## CORS

- `cors_origins` / `GW_CORS_ORIGINS`에 `"*"`가 있으면 게이트웨이는 기동을 거부한다.
- 라우트 단위 거부: 경로 세그먼트에 `actions`, `revise`, `reissue`, `token`, `tokens`, `confirm`, `confirmations`가 하나라도 있으면(대소문자 무시) CORS 허용 헤더를 절대 내보내지 않는다. preflight는 403. 아직 없는 라우트도 이 규칙으로 덮는다.
