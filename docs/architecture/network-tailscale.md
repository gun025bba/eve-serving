# 네트워크 · Tailscale

![network](diagrams/network-tailscale.svg)

원본: [`diagrams/network-tailscale.excalidraw`](diagrams/network-tailscale.excalidraw)

- 외부 노출은 `tailscale serve --bg --https=443 http://127.0.0.1:8000` 하나 (tailnet 전용, **Funnel 아님**). 주소 형식: `https://<host>.<tailnet>.ts.net/ui/`.
- `tailscale serve`가 `Tailscale-User-Login` 헤더를 주입 → gateway는 TCP peer가 loopback이고 `allowed_users`에 있을 때만 신뢰. 비 loopback 요청에서는 헤더 제거.
- 레거시: `com.llmserving.gateway-ts`가 `<tailscale-ip>:8000`에 직접 바인딩 — serve 전환 시 bootout (롤백 절차는 루트 README).
- mlx(8080), SearXNG(8888), PostgreSQL은 loopback 전용.
- TBD: tailnet ACL 정책 (저장소에 없음).
