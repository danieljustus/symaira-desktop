# SymDesk Server

Set `server_token` to a random value with at least 32 characters before starting the app. The vault, originals, index and durable OCR queue live below `/data` and survive restarts.

Expose TCP port `8787` only to your trusted LAN or VPN. Connect the SymDesk Mac and iOS apps with `http://HOME-ASSISTANT-IP:8787` and the same token. For access outside your LAN, put SymDesk behind HTTPS (for example a VPN or reverse proxy); never forward the plain HTTP port directly to the internet.

Keep `local_processing` disabled when Home Assistant runs on a small Raspberry Pi and start `symdesk worker` on a MacBook instead. Enable it to process OCR in the app container with Tesseract. Ollama must be reachable from this container when `worker_engine` is `ollama`.
