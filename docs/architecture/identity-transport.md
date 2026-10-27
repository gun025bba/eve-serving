# 신원 전달 설계 (4.0.0 재설계: TCP 루프백 + 세션 자격 증명)

상태: **설계 확정(보안 합의 2026-10-27: 세션 수명·페어링 잠금·/health·비밀 보관)**. 4.0.0의 unix 소켓 방식(`docs/runbooks/4.0.0-unix-socket.md`)을 대체한다.

## 배경
- 4.0.0 컷오버는 tailnet 502로 실패해 3.1.0으로 롤백했다. 샌드박스된 macOS Tailscale(시스템 익스텐션)은 serve 대상으로 `unix:` 소켓에 연결하지 못한다. 공식 문서도 serve 프록시는 loopback HTTP만 지원한다고 적고 있다 (#23 백엔드 조사 코멘트).
- `tailscale serve`는 **임의 헤더를 주입할 수 없다**. 그래서 "serve가 넣어 주는 공유 비밀 헤더"는 만들 수 없다.
- loopback TCP에서는 host의 어떤 로컬 프로세스든 `Tailscale-User-*` 헤더를 위조할 수 있다(serve가 클라이언트 헤더를 지우는 건 tailnet 경로뿐이다).

## 결정
1. **바인딩**: gateway는 `127.0.0.1:<port>`(선택적으로 `[::1]:<port>`)에만 바인딩한다. 다른 주소(0.0.0.0, tailnet IP, LAN)가 설정되면 **기동 실패**. 레거시 tailnet IP 리스너는 없다. `tailscale serve https:443 / -> http://127.0.0.1:<port>`, **Funnel off**(funnel nodeAttr 제거 유지, runbook §10).
2. **인가 근거 = 클라이언트 자격 증명만**: 모든 요청은 (a) 브라우저 세션 쿠키 또는 (b) API 키 Bearer로 인증한다. 비교는 해시 후 constant-time(`subtle.ConstantTimeCompare`). 이것이 "공유 비밀"의 역할을 한다.
3. **`Tailscale-User-*` 헤더는 인가에 쓰지 않는다**. 값은 로그용 참고 필드(`ts_login_hint`)로만 남기고, 신원·users.id 매핑·allowed_users 판정에 쓰지 않는다. `ProviderTailscale`과 소켓 신뢰 경로(`ViaTrustedSocket`)는 제거한다.
4. **브라우저 세션 발급(페어링)**: host에서 관리자가 CLI(`eve-gw pair --user <login>`)로 페어링 코드를 만든다. 코드: **CSPRNG로 생성한 Crockford base32 8자(40비트)**, 표시 형식 `XXXX-XXXX`, 유효 10분, **1회용**(성공·만료·잠금 시 즉시 폐기). 제출: `POST /v1/pair` — **비인증 엔드포인트**이며 실패 **5회면 10분 잠금 + 해당 코드 무효화**(코드별·전역 카운터), 응답은 성공/실패만 구분. 사용자는 Serve URL에서 코드를 입력하고, gateway는 256비트 무작위 세션 토큰을 `HttpOnly; Secure; SameSite=Strict; Path=/` 쿠키로 발급한다. DB에는 토큰의 해시만 `sessions(user_id, token_hash, created_at, last_seen, revoked_at)`로 저장한다. 세션은 기존 `users.id`에 묶인다. 상태 변경 요청은 기존 CSRF·Origin 검사를 그대로 받는다.
5. **Host/Origin**: `Host`는 allowlist(`<TS_HOST>`, `localhost`, `127.0.0.1`)만 허용하고 나머지는 421/400. CORS 와일드카드 금지(동일 출처만).
6. **비인증 엔드포인트(전체 목록)**: `GET /health`(응답은 정확히 `{"status":"ok"}`, 버전·설정·DB 상태 없음)와 `POST /v1/pair`(위 잠금 규칙). 그 외는 전부 인증 필요.
6-1. **쿠키 없는 API 클라이언트**: 반드시 `Authorization: Bearer <API 키>`. 쿠키도 Bearer도 없으면 401, Bearer로 브라우저 세션 전용 라우트(`/v1/memory*`, approve 등)를 부르면 403 `scope_denied`.
6-2. **세션 수명(확정)**: 유휴 30일, 절대 180일. 만료 시 재페어링.
7. **비밀 보관(확정)**: API 키 해시, 세션 페퍼 키, 감사 HMAC 키는 저장소 밖 `/opt/eve-gw/etc/` **0600 `_evegw`**. keychain은 선택. root·`_evegw`가 읽을 수 있는 위험은 수용(보안 합의). 로그에는 마스킹(앞 4자 + `…`). 필요한 키가 없거나 권한이 0600보다 넓으면 **기동 거부(fail-closed)**.
8. **회전 절차**: (API 키) 새 키 추가 → 클라이언트 교체 → 옛 키 제거 → 재시작 로그 확인. (세션 페퍼) 교체 시 모든 세션이 무효화되므로 재페어링 안내 후 교체. (개별 세션) `eve-gw sessions revoke <id>` 또는 UI 설정에서 기기 로그아웃.

## 로컬 위협 모델
- loopback 포트에는 host의 모든 로컬 프로세스(다른 사용자 계정, 에이전트, 개발 도구, 브라우저 확장이 띄운 로컬 서버 등)가 접근할 수 있다.
- 이들은 헤더는 위조할 수 있지만 세션 토큰·API 키는 모른다. 그래서 인가는 막힌다. 남는 위험: 0600 비밀 파일을 읽을 수 있는 계정(root, 서비스 계정)과 비인증 `/health` 조회. 이는 host 신뢰 경계 안의 위험으로 수용한다.
- 서비스 계정 `_evegw`의 파일 권한 설계(runbook §0)는 그대로 유지한다. 소켓 디렉터리·`_evegw` 그룹 규칙은 더 이상 필요 없다.

## users.id 영향 (5.0.0 #54와의 관계)
- 기존 `users(id, login, provider='tailscale')` 행은 **그대로 유지**한다. 페어링 시 `--user <login>`으로 기존 행을 찾아 같은 `users.id`에 세션을 묶는다 → memory 파일 키(`<users.id>.json`)·settings 이전 불필요.
- "브라우저 세션 전용" 규칙은 **페어링 세션 쿠키 인증**을 뜻하도록 정의만 바뀐다. Bearer는 계속 403 `scope_denied`.
- 1회 마이그레이션(goose): `sessions` 테이블 추가. 데이터 삭제 없음.

## 남은 공백
- 기존 PWA 사용자 전원이 컷오버 후 1회 재페어링해야 한다(UX 안내 필요).

## 확장 청사진
- Tailscale이 샌드박스 변형에서 `unix:` 대상을 지원하거나 오픈소스 tailscaled로 전환하면, 소켓 리스너를 다시 추가하고 **그때만** `Tailscale-User-Login`을 페어링 없는 1차 신원으로 승격할 수 있다(`auth.providers` 순서 설정). 세션 쿠키 경로는 그대로 둔다.
- 대안: gateway가 tailscale LocalAPI `whois`로 `X-Forwarded-For`를 검증하는 방식은 로컬 프로세스가 XFF도 위조할 수 있어 채택하지 않는다.

## SemVer
4.0.0 MAJOR 유지(인증 경계 변경). 추가로 기존 브라우저 사용자 재페어링이 필요하다는 점이 릴리스 노트에 들어가야 한다.
