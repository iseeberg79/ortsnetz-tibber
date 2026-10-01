# ortsnetz-tibber

Kleiner Go-Dienst, der die Phasenspannungen eines **Tibber Pulse** liest und alle fünf Minuten an
[ortsnetz-auslastung.de](https://www.ortsnetz-auslastung.de) meldet. Die Seite zeigt freiwillig
übermittelte Spannungsmessungen aus Ortsnetzen in Deutschland, Österreich und der Schweiz auf einer Karte.

Das Projekt ist unabhängig von Tibber und vom Betreiber von ortsnetz-auslastung.de.

## Voraussetzungen

- Ein Tibber-Konto mit Pulse und aktivierten Echtzeitdaten.
- Ein **Zähler, der Phasenspannungen sendet.** Der Pulse reicht nur durch, was der Zähler ausgibt.
  Viele Zähler senden im Standard-Datensatz nur Zählerstände und Leistung, dann liefert Tibber für
  `voltagePhase1/2/3` den Wert `null` und der Dienst meldet nichts. Oft lässt sich beim
  Messstellenbetreiber ein erweiterter Datensatz freischalten (meist per PIN).
- Ein persönlicher Tibber-Access-Token aus dem [Developer-Portal](https://developer.tibber.com).
- Go 1.23 oder neuer zum Bauen.

## Funktionsweise

1. Der Dienst abonniert per Websocket (`graphql-transport-ws`) die Live-Messung deines Tibber-Homes.
2. Er hält den jeweils letzten Wert vor.
3. Sofort nach dem ersten gültigen Wert und danach alle fünf Minuten sendet er `POST /v1/measurements`
   an ortsnetz-auslastung.de.

Gemeldet werden nur Messungen, die plausibel sind: L1 muss zwischen 150 und 300 V liegen. L2 und L3
werden mitgesendet, wenn vorhanden und plausibel, bei einphasigen Anschlüssen als `-1`. Liegt kein
aktueller Wert vor, wird die Meldung übersprungen. Reißt die Verbindung zu Tibber ab, verbindet sich
der Dienst nach 30 Sekunden neu.

Die Koordinaten werden vor dem Senden auf drei Nachkommastellen gerundet (etwa 100 m). Mehr verlässt
den Rechner nicht: Zeitstempel, gerundete Koordinaten, die Spannungen, `smartmeter_model: "Tibber Pulse"`
und die Version des Dienstes.

## Konfiguration

Alles läuft über Umgebungsvariablen:

| Variable | Pflicht | Bedeutung |
|---|---|---|
| `TIBBER_TOKEN` | ja | Persönlicher Access-Token |
| `TIBBER_HOME_ID` | nein | Home-ID. Fehlt sie, nimmt der Dienst das einzige Home des Tokens. Bei mehreren Homes bricht er mit der Liste der IDs ab. |
| `LATITUDE`, `LONGITUDE` | ja | Standort in Dezimalgrad |
| `API_URL` | nein | Anderes Ziel statt `https://www.ortsnetz-auslastung.de/v1/measurements`, zum Beispiel für Tests |

## Bauen und starten

```sh
go build -o ortsnetz-tibber .

TIBBER_TOKEN=… LATITUDE=52.520 LONGITUDE=13.405 ./ortsnetz-tibber
```

Beispiel für einen systemd-Dienst (`/etc/systemd/system/ortsnetz-tibber.service`), die Zugangsdaten
liegen in einer Datei mit Rechten `600`:

```ini
[Unit]
Description=Tibber-Spannungen an ortsnetz-auslastung.de melden
After=network-online.target
Wants=network-online.target

[Service]
EnvironmentFile=/etc/ortsnetz-tibber.env
ExecStart=/usr/local/bin/ortsnetz-tibber
Restart=on-failure
RestartSec=30

[Install]
WantedBy=multi-user.target
```

`/etc/ortsnetz-tibber.env`:

```
TIBBER_TOKEN=…
LATITUDE=52.520
LONGITUDE=13.405
```

## Logausgaben

| Meldung | Bedeutung |
|---|---|
| `ok L1=… L2=… L3=… V: …` | Messung wurde angenommen (HTTP 202) |
| `kein aktueller Messwert, überspringe` | In den letzten fünf Minuten kam keine plausible Messung an, zum Beispiel weil Tibber `null` liefert |
| `abgelehnt (422): …` | Die API hat Felder oder Wertebereiche abgelehnt |
| `Standort gesperrt (403): …` | Die API nimmt für diesen Standort keine Messungen an, der Dienst beendet sich |
| `tibber: …; neuer Versuch in 30s` | Verbindung zu Tibber abgebrochen oder abgelehnt |

## Datenschutz

Der Dienst überträgt Spannungen und einen auf etwa 100 m gerasterten Standort an einen Dritten. Der
Endpunkt braucht keine Anmeldung. Setze ihn nur für Anschlüsse ein, für die du dazu berechtigt bist,
und halte den Tibber-Token geheim: Er gibt Lesezugriff auf deine Verbrauchsdaten.

## API von ortsnetz-auslastung.de

`POST https://www.ortsnetz-auslastung.de/v1/measurements`, JSON, keine Authentifizierung.

| Feld | Pflicht | Hinweis |
|---|---|---|
| `observed_at` | ja | ISO-8601 mit Zeitzone |
| `latitude`, `longitude` | ja | Dezimalgrad |
| `l1_v` | ja | Volt, größer 0 und höchstens 500 |
| `l2_v`, `l3_v` | ja | Volt oder `-1` bei einphasigem Anschluss |
| `grid_frequency_hz`, `plant_capacity_kwp`, `pv_forecast_kwh` | nein | Dieser Dienst sendet sie nicht |
| `smartmeter_model`, `integration_version` | nein | |

Antworten: `202` angenommen, `422` ungültige Felder, `403` Standort gesperrt. Details und weitere
Clients (Home Assistant, Shelly, ioBroker) im
[Projekt des Betreibers](https://github.com/thomaslehmann1234/datenerfassung-ortsnetz-auslastung).

## Entwicklung

```sh
go vet ./...
go test ./...
```

Der Test deckt die Umwandlung der Tibber-Messwerte ab (dreiphasig, einphasig, fehlendes L1,
Werte außerhalb von 150–300 V).
