---
trigger: always_on
---

---
description: "Erlaubte Defaults"
triggers:
  - glob: "**/*"
permissions:
  allow:
    # --- Git Befehle ---
    - "command(git status .*)"
    - "command(git diff .*)"
    - "command(git log .*)"
    - "command(git fetch .*)"
    - "command(regex:^git (add|pull).*)"

    # --- Container / Podman ---
    - "command(podman --version)"
    - "command(regex:^podman (build|compose).*)"

    # --- System & Text-Utilities ---
    - "command(regex:^find .*)"
    - "command(regex:^file .*)"
    - "command(regex:^ffmpeg .*)"
    - "command(regex:^ffprobe .*)"
    - "command(regex:^gcc .*)"
    - "command(regex:^gh .*)"
    - "command(regex:^lsof .*)"
    - "command(regex:^pwd .*)"

    - "command(regex:^grep .*)"
    - "command(regex:^strings .*)"
    - "command(regex:^ls.*)"
    - "command(regex:^head .*)"
    - "command(regex:^mkdir .*)"
    - "command(regex:^sort .*)"
    - "command(regex:^wc .*)"
    - "command(regex:^sleep .*)"

    # --- macOS / Binary Analyse & Netzwerk ---
    - "command(regex:^nm .*)"
    - "command(regex:^otool .*)"
    - "command(regex:^ping .*)"
---

# Projekt-Richtlinien & Tool-Freigaben

Dieses Dokument autorisiert den Agenten zur Nutzung der oben definierten CLI-Werkzeuge.