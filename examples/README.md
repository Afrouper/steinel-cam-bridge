# Standalone Docker Compose Multi-Kamera Setup

Dieses Verzeichnis enthält eine schlüsselfertige Vorlage für den Betrieb der **Steinel CAM Bridge** im Multi-Kamera-Modus unter Docker Compose.

## Dateien

- [`docker-compose.yml`](docker-compose.yml): Gehärtete Compose-Konfiguration (`network_mode: host`, Non-Root User 1000, Read-Only Filesystem, Capabilities Drop).
- [`config.json`](config.json): Beispiel-Konfigurationsdatei für den gleichzeitigen Betrieb einer **Steinel L 625 CAM SC** (Haustür) und einer **Steinel L 620 CAM** (Garten).

## Schnellstart

1. **Konfiguration anpassen:**
   Öffnen Sie `config.json` und tragen Sie Ihre IP-Adressen und Zugangsdaten ein:
   - **Haustür (L 625 CAM SC):** Tragen Sie die lokale IP und den `qr_code`-String aus der Steinel App ein.
   - **Garten (L 620 CAM / XLED CAM 1):** Tragen Sie die lokale IP und das in der Steinel App vergebene Gerätepasswort unter `camera_password` ein.
   - **MQTT Broker:** Tragen Sie die Verbindungsdaten Ihres Brokers ein (z. B. `tcp://192.168.1.50:1883`).

2. **Container starten:**
   ```bash
   docker compose up -d
   ```

3. **Logs prüfen:**
   ```bash
   docker compose logs -f
   ```

4. **Kamera-Streams abrufen:**
   - **Haustür:** `rtsp://<Host-IP>:8554/steinel-haustuer`
   - **Garten:** `rtsp://<Host-IP>:8554/steinel-garten`
   - **ONVIF:** `http://<Host-IP>:8000/onvif/device_service` (beide Kanäle werden über diesen gemeinsamen Endpunkt bereitgestellt).
