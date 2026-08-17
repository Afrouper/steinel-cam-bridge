# Home Assistant Add-on: Steinel CAM Bridge

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
| `camera_ip` | String | `192.168.1.100` | Lokale IP-Adresse der Steinel-Kamera im Heimnetz |
| `qr_code` | String | `""` | QR-Code Payload zum automatischen Pairing (initial) |
| `resolution` | Liste | `1080p` | Standardauflösung (`1080p`, `720p`, `360p`) |
| `audio_codec` | Liste | `aac` | Audio-Codec des RTSP-Streams: `aac` (nativ transkodiert) oder `pcmu` |
| `rtsp_port` | Port | `8554` | RTSP Server Port |
| `onvif_port` | Port | `8000` | ONVIF HTTP Service Port |
| `reset_pairing` | Boolean | `false` | Setzen Sie diese Option auf `true`, um den gespeicherten Schlüssel zu löschen und ein erneutes Pairing mit dem angegebenen `qr_code` zu erzwingen |

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
Wenn das offizielle **Mosquitto MQTT Add-on** in Home Assistant installiert ist, verbindet sich dieses Add-on vollautomatisch ohne manuelle Passworteingabe (`mqtt:want`). Unter **Einstellungen ➔ Geräte & Dienste ➔ MQTT** erscheint automatisch die Steinel-Außenleuchte mit allen Licht- und Dimm-Entitäten!

---

## 🍏 Scrypted & Apple HomeKit Integration
Wenn Sie Scrypted (als Add-on oder separat) nutzen:
1. Im Scrypted **ONVIF Plugin** auf *Add Camera* klicken (`127.0.0.1`, Port `8000`).
2. Scrypted empfängt sofort H.264 Video, natives AAC-Audio und unterstützt Gegensprechen.
