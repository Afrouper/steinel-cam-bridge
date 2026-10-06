---
trigger: always_on
---

---
description: "Erlaubt CGO-Builds und Go-Tests mit dem lokalen SDK"
triggers:
  - glob: "**/*"
permissions:
  allow:
    # --- Go & CGO Befehle (inkl. Pfad-Variablen und Verzeichniswechsel -C) ---
    - "command(regex:^.*go (test|build|doc|version|vet|get|list).*)"
    - "command(regex:^(DYLD_LIBRARY_PATH="(\$\(pwd\)|\`pwd\`)/\.sdk/lib"\s+)?go (test|build|doc|version|vet|get|list).*)"
    - "command(gofmt)"
    - "command(regex:^go mod tidy .*)"
    - "command(regex:^gofmt .*)"
    - "command(regex:^golangci-lint.*)"
---

# Go & CGO Projekt-Richtlinien

Dieses Projekt verwendet ein lokales C-SDK im Ordner `.sdk/`. 
Der Agent darf Go-Builds und Go-Tests mit entsprechenden Umgebungsvarianten autonom ausführen.

- Nutze beim Kompilieren der `steinel-bridge` wenn notwendig die CGO-Flags für `.sdk/lib` und `.sdk/include`.
- Stelle sicher, dass bei Testausführungen `DYLD_LIBRARY_PATH` korrekt auf den lokalen SDK-Library-Pfad zeigt.