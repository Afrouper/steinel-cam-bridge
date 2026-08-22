# Changelog

Alle wichtigen Änderungen für das **Steinel CAM Bridge** Add-on werden hier dokumentiert.

## 0.10.3

- 🐛 **Stabilitäts-Fix**: Behebung möglicher Nil-Pointer Panics beim Beenden von RTSP-Sessions und sauberes Schließen von Hintergrund-Goroutinen.

## 0.10.2

- 💡 **Home Assistant MQTT**: Helligkeitssteuerung für Hauptlicht und Grundlicht auf standardisierte `number`-Entitäten optimiert.
- 🧹 **Throttled Logging**: Gedrosseltes Logging bei Verbindungsabbrüchen.

## 0.10.1

- 🔒 **Sicherheits-Fix**: Berechtigungs- und Pfadkorrekturen für das Google Distroless Runtime-Image.
- ⚠️ **Konfigurationsvalidierung**: Pflichtprüfung der Kamera-IP-Adresse beim Start.

## 0.10.0

- 🚀 **Nativer Go-Launcher**: 100 % autarkes Single-Binary mit automatischem Nabto SDK-Download ohne externe Paketabhängigkeiten.
- 📦 **Google Distroless**: Minimales und gehärtetes Container-Image (Größe auf ca. 56 MB reduziert).

## 0.9.9

- 📡 **MQTT Auto-Discovery**: Automatische Erkennung des Home Assistant Mosquitto MQTT-Brokers über die Supervisor API.

## 0.9.2

- 🗄️ **SD-Karten REST API**: Direkte 1:1 REST-API (Port 8000) für Ereignislisten, Snapshots und Video-Downloads mit Hardware-Überlastungsschutz.
