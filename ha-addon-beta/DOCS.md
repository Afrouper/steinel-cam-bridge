# Home Assistant Add-on: Steinel CAM Bridge - BETA

Standalone ONVIF, 2-Way Audio (Gegensprechen) & MQTT Bridge für die **Steinel L 625 CAM SC** (oder ähnlich) Außenleuchte.

---

## 🚀 Schnellstart

1. **Kamera-IP** eintragen (z. B. `192.168.1.100`).
2. **QR-Code Payload** aus der Steinel App ("Kamera teilen") in das Feld `qr_code` einfügen.
   - Format: `did=de-xxxxxxx,pid=pr-xxxxx,sct=xxxx,pairPwd=xxxx`
3. Auf **Speichern** und anschließend auf **Starten** klicken!

Der QR Code wird initial für das Pairing benötigt. Danach muss er nicht mehr zwingend angegeben werden da das Schlüsselmaterial lokal gespeichert wird.

---

## ⚙️ Konfiguration

| Option | Typ | Standard | Beschreibung |
|---|---|---|---|
| `camera_ip` | String | *(Pflichtfeld)* | Lokale IP-Adresse der Steinel-Kamera im Heimnetz (z. B. `192.168.1.100`) |
| `camera_type` | Liste | `auto` | Kameramodell: `auto` (automatische Erkennung), `l625` (L 625 CAM SC), `l620` (L 620 CAM / XLED CAM 1) |
| `camera_user` | String | `admin` | Benutzername für L 620 CAM (Standard: `admin`) |
| `camera_password` | String | `""` | Geräte-Passwort für L 620 CAM (in der Steinel App vergeben) |
| `qr_code` | String | `""` | QR-Code Payload zum automatischen Pairing der L 625 CAM SC |
| `resolution` | Liste | `1080p` | Standardauflösung (`1080p`, `720p`, `360p`) |
| `audio_codec` | Liste | `aac` | Audio-Codec des RTSP-Streams: `aac` (nativ transkodiert) oder `pcmu` |
| `rtsp_port` | Port | `8554` | RTSP Server Port |
| `onvif_port` | Port | `8000` | ONVIF HTTP & SD-Karten REST API Service Port |
| `sdcard_sync_interval` | Ganzzahl | `60` | Intervall in Sekunden für die Hintergrundabfrage neuer SD-Karten-Aufnahmen (5–300 s) |
| `nabto_driver` | Liste | `cgo` | Nabto-Treiber-Engine für L 625: `cgo` (offizielles C-SDK, empfohlen & Standard) oder `pure` (nativer Go-Stack, experimentell) |
| `reset_pairing` | Boolean | `false` | Setzen Sie diese Option auf `true`, um den gespeicherten Schlüssel zu löschen und ein erneutes Pairing der L 625 mit dem angegebenen `qr_code` zu erzwingen |
| `debug` | Boolean | `false` | Ausführliches Debug-Logging für Diagnosezwecke aktivieren |
| `mqtt_broker` | String | `""` | Optional: Benutzerdefinierte MQTT-Broker-URL (z. B. `tcp://192.168.1.50:1883`). Leer lassen für automatische Erkennung des Home Assistant Mosquitto Brokers |
| `mqtt_user` | String | `""` | Optional: MQTT Benutzername |
| `mqtt_password` | Passwort | `""` | Optional: MQTT Passwort |
| `mqtt_topic_prefix` | String | `steinel` | MQTT Basis-Topic |
| `mqtt_discovery_prefix` | String | `homeassistant` | Home Assistant Auto-Discovery Prefix |

---

## 📷 Einrichtung Steinel L 620 CAM (Generation 1)
Für die Vorgängermodelle **L 620 CAM** und **XLED CAM 1**:
1. Tragen Sie die **`camera_ip`** ein.
2. Geben Sie Ihr in der Steinel-App vergebenes **`camera_password`** ein (Benutzer `camera_user` bleibt `admin`).
3. Setzen Sie optional `camera_type: l620`.
4. Klicken Sie auf **Speichern** und **Starten**. Die Bridge aktiviert RTSP auf der Kamera vollautomatisch und bindet Video, 2-Wege-Audio und alle MQTT-Entitäten lokal ein!

---

## 🔄 Kamera neu pairen / Pairing-Reset
Falls die Kamera zurückgesetzt wurde, das Pairing-Passwort geändert wurde oder Sie einen neuen QR-Code nutzen möchten:
1. Fügen Sie den neuen `qr_code` in das Konfigurationsfeld ein.
2. Setzen Sie den Schalter **`reset_pairing: true`**.
3. Klicken Sie auf **Speichern** und starten Sie das Add-on neu.
   - Das Add-on löscht automatisch den alten gespeicherten Schlüssel, erzeugt einen frischen Private Key und koppelt sich neu mit der Kamera.
4. Nach erfolgreicher Kopplung können Sie `reset_pairing` wieder auf `false` zurückstellen.

---

## 🏠 Home Assistant MQTT Integration
Wenn das offizielle **Mosquitto MQTT Add-on** in Home Assistant installiert ist, verbindet sich dieses Add-on vollautomatisch ohne manuelle Passworteingabe (`mqtt:want`). (*Hinweis: Nach der Erstinstallation des Add-ons bitte einmalig das Mosquitto Broker Add-on neu starten, damit Home Assistant die Dienstverknüpfung initial freischaltet.*) Unter **Einstellungen ➔ Geräte & Dienste ➔ MQTT** erscheint automatisch die Steinel-Außenleuchte mit allen Licht- und Dimm-Entitäten!

---

## 🍏 Scrypted & Apple HomeKit Integration
Wenn Sie Scrypted (als Add-on oder separat) nutzen:
1. Im Scrypted **ONVIF Plugin** auf *Add Camera* klicken (`127.0.0.1`, Port `8000`).
2. Scrypted empfängt sofort H.264 Video, natives AAC-Audio und unterstützt Gegensprechen.
