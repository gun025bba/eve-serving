# Eve

Eve는 개인용 로컬 LLM 서비스입니다. 로컬에서 서빙하는 모델에 웹 검색 등 툴을 연결한 비공개 어시스턴트이며, 외부 클라우드 모델에 의존하지 않습니다.

## 구성

| 구성 요소 | 설명 |
|---|---|
| 게이트웨이 | Go (`cmd/gateway`, `internal/`). 인증, 큐잉, 스트리밍(SSE), 대화 저장, 툴 호출, 지표(`/metrics`)를 담당합니다. 설정은 `config.yaml`, 환경변수 `GW_<KEY>`로 덮어씁니다. |
| 모델 서빙 | `mlx_lm.server`. OpenAI 호환 API를 localhost에서만 제공하고, 게이트웨이만 접근합니다. |
| DB | PostgreSQL + pgvector. 스키마는 goose 마이그레이션(게이트웨이에 내장, `make migrate`)으로 관리합니다. |
| 툴 플랫폼 | 툴 레지스트리, 확인 카드, 감사 로그, egress 정책. [docs/tool-platform.md](docs/tool-platform.md) 참고. |

자세한 설계는 [docs/architecture](docs/architecture/README.md)를 보세요.

- [게이트웨이](docs/architecture/gateway.md) · [모델 서빙](docs/architecture/model-serving.md) · [PostgreSQL](docs/architecture/postgresql.md) · [툴 플랫폼](docs/architecture/tool-platform.md)
- [웹 검색(SearXNG)](docs/architecture/searxng-web-search.md) · [메모리](docs/architecture/memory.md) · [선톡](docs/architecture/proactive-messaging.md) · [웹 UI](docs/architecture/web-ui.md)
- [네트워크](docs/architecture/network-tailscale.md) · [CI/배포](docs/architecture/ci-deploy.md) · [평가](docs/architecture/eval.md)
- API: [docs/threads-api.md](docs/threads-api.md), DB 모델: [docs/db-model.md](docs/db-model.md)

## 실행·배포

```bash
make test            # 단위 테스트
make build           # bin/gateway 빌드 (버전은 git 태그에서 주입)
make run             # 로컬 실행 (기본 127.0.0.1:8001)
```

로컬 개발 시작 주의: 기본 `auth.providers`에 `tailscale-identity`가 있는데 `allowed_users`가 비어 있으면
게이트웨이가 **기동을 거부**합니다(#58). 둘 중 하나를 지정하세요.

```bash
GW_AUTH_PROVIDERS=api-key make run                 # API 키만 (로컬 개발 권장)
GW_ALLOWED_USERS=<login>@github make run           # Tailscale 신원 인증까지 테스트할 때
```

서버 배포(launchd):

```bash
git pull --ff-only && make build
deploy/check-auth-env.sh <path-to-gateway.plist> [<config.yaml>] || exit 1   # 빈 허용 목록 → 크래시 루프 방지
launchctl bootout   gui/$(id -u)/<gateway-label>
launchctl bootstrap gui/$(id -u) <path-to-gateway.plist>
curl -s http://127.0.0.1:<port>/health     # "version" 확인
```

launchd plist 템플릿과 설치 스크립트는 `deploy/`에 있습니다.

롤백: 이전 바이너리 또는 이전 태그로 되돌립니다.

```bash
git checkout vX.Y.Z && make build            # 또는 보관해 둔 이전 바이너리로 교체
launchctl bootout   gui/$(id -u)/<gateway-label>
launchctl bootstrap gui/$(id -u) <path-to-gateway.plist>
curl -s http://127.0.0.1:<port>/health
```

## 보안

브라우저 쓰기 요청의 CSRF 방어(`X-Eve-Request`, Origin 허용 목록 `GW_ALLOWED_ORIGINS`)는 [docs/security.md](docs/security.md) 참고.

## 버전 정책

SemVer(`MAJOR.MINOR.PATCH`)를 따르고, 릴리스는 `vX.Y.Z` 태그로 표시합니다.

- 메이저(호환되지 않는 API 변경, 되돌릴 수 없는 마이그레이션, 모델 교체, 노출·보안 경계 변경): 배포 전 소유자 승인 필수
- 마이너·패치: 승인 없이 배포하고 배포 후 보고

모든 배포는 버전, 변경 사항, 롤백 방법을 남깁니다. 자세한 규칙은 [docs/versioning.md](docs/versioning.md)를 보세요.
