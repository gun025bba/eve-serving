# AGENTS.md — eve-serving (백엔드)

Eve 게이트웨이(Go, `cmd/gateway`, `internal/`), 모델 서빙 설정, 배포 스크립트(`deploy/`), 설계 문서(`docs/`)가 있는 저장소예요. 구조와 설계는 [README.md](README.md)와 [docs/architecture](docs/architecture/README.md)를 먼저 보세요.

## 빌드·테스트
```bash
make build                      # bin/gateway 빌드
make test                       # go test ./...
go vet ./... && test -z "$(gofmt -l .)"   # 포맷·정적 검사 (CI와 동일)
go run ./cmd/eve-policy verify  # 툴 정책 검증
go test -race -count=1 ./...
```
Postgres 통합 테스트는 CI에서 돌아가요. 로컬에서는 테스트용 DB(`GW_TEST_DATABASE_URL`)에만 연결하세요.

## 작업 방식
- 이슈 하나에 PR 하나. 브랜치는 `feat/…`, `fix/…`, `docs/…` 형태로, 기능 변경에는 테스트를 같이 넣어요.
- CI(`ci.yml`)가 통과해야 머지해요. force-push 금지.
- 메이저 변경(호환되지 않는 API 변경, 되돌릴 수 없는 마이그레이션, 모델 교체, 노출·보안 경계 변경)은 보안 리뷰와 소유자 승인이 필요해요. [docs/versioning.md](docs/versioning.md) 참고.
- 비밀값, 실제 주소·호스트 이름, 서버 머신 이름을 커밋하지 않아요. 예시는 `<gateway-host>` 같은 자리표시자를 써요.
- 봇이 만든 커밋의 트레일러는 `Co-authored-by: Grok <grok-bot@noreply.invalid>` 하나만 써요. Codex 커밋에는 추가 트레일러를 넣지 않아요.

## Codex 범위 (코드 구현만)
할 일: 이슈에 적힌 범위의 코드 구현, 테스트 추가, 리팩터링. 끝나면 PR을 열고 멈춰요.

하지 않는 일:
- 배포, 서버·launchd·네트워크 설정 변경
- 비밀값 다루기(키, 토큰, `.db_env` 등)
- 실제 DB에 마이그레이션 실행 (마이그레이션 파일 작성은 가능, 실행은 안 해요)
- `.github/workflows/`, `deploy/` 등 CI/CD 수정
- 이슈 범위를 벗어난 변경
