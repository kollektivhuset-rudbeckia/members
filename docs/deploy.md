# Flytta registret till servern

Registret körs som en container och håller allt i en enda SQLite-fil. Det gör
flytten kort — men det finns exakt en sak som är svår att göra om, och det är
databasen.

## Vad som är tillstånd, och vad som inte är det

| | Var det bor | Går det att göra om? |
|---|---|---|
| **Registret** — 114 medlemmar, avgifter, förslag, loggen | Docker-volymen `members_members-data` | Delvis. En ny import ger tillbaka medlemmarna men inte loggen, inte avgifterna, och alla får nya id:n |
| Hemligheter | `.env` | Nej — klienthemligheten går bara att byta, inte hämta |
| Tjänstekontots nyckel | `secrets/service-account.json` | Nej — bara ersätta med en ny |
| Föreningens uppgifter | `config.yaml` | Ja, men den är handskriven |
| Programmet | `ghcr.io/kollektivhuset-rudbeckia/members` | Ja, hämtas |
| Grupper, kontakter, kalkylark | Google | Ja — de byggs om från registret vid nästa körning |

Allt utom det första kan skrivas om på tio minuter. Databasen kan det inte, så
den är det som flyttas snarare än görs om.

## Det som ska kopieras

Fyra filer och en volym:

```
.env                          # hemligheter — ändra BASE_URL, se nedan
secrets/service-account.json  # nyckeln, ska ägas av uid 65532
config.yaml                   # föreningen
docker-compose.yml            # hur den körs
                              # + volymen members_members-data
```

`.gitignore` håller de två första utanför repot med flit. Skicka dem inte i en
chatt — `scp` dem, eller lägg dem i husets lösenordshanterare.

### Databasen

Stoppa containern först. SQLite kör i WAL-läge, så en kopia av en igångvarande
databas kan sakna det senaste — och `.db-wal`-filen är lika viktig som `.db`.
Att stoppa är enklare än att komma ihåg det.

```bash
# På den gamla maskinen
docker compose stop members
docker run --rm -v members_members-data:/d -v "$PWD:/backup" alpine \
    tar czf /backup/members-data.tgz -C /d .

scp members-data.tgz .env config.yaml docker-compose.yml server:/srv/members/
scp secrets/service-account.json server:/srv/members/secrets/
```

```bash
# På servern
cd /srv/members
docker volume create members_members-data
docker run --rm -v members_members-data:/d -v "$PWD:/backup" alpine \
    tar xzf /backup/members-data.tgz -C /d
docker run --rm -v members_members-data:/d alpine chown -R 65532:65532 /d
```

Volymens namn kommer av katalogens namn plus namnet i `docker-compose.yml`.
Ligger projektet i `/srv/members` blir det `members_members-data`; ligger det
någon annanstans heter det något annat, och då är det namnet som ska användas
i kommandona ovan. `docker volume ls` säger vad det blev.

### Nyckelns ägare

Containern kör som `uid 65532` och kan inte läsa en fil som ägs av någon
annan med `600`. Det syns som `read GOOGLE_SERVICE_ACCOUNT_FILE: permission
denied` vid start.

```bash
sudo chown 65532:65532 secrets/service-account.json
sudo chmod 600 secrets/service-account.json
```

### `.env` på servern

En rad ska ändras, och en bör läggas till:

```bash
BASE_URL=https://medlemmar.rudbeckia.nu    # inte localhost
SESSION_SECRET=<slumpa en>                  # annars loggas alla ut vid varje deploy
```

Slumpa den med `openssl rand -base64 32`.

`GOOGLE_SERVICE_ACCOUNT_FILE` ska vara `/secrets/service-account.json` —
sökvägen **inne i containern**, inte på servern. `docker-compose.yml` monterar
`./secrets` på `/secrets`.

### Adressen

`BASE_URL` måste stämma med den auktoriserade redirect-URI:n i OAuth-klienten,
annars vägrar Google inloggningen med `redirect_uri_mismatch`. Se
[google-workspace.md](google-workspace.md), del 3b.

Framför containern, en proxy som terminerar TLS. Caddy:

```
medlemmar.rudbeckia.nu {
    reverse_proxy localhost:8082
}
```

Utan `https://` i `BASE_URL` sätts inte sessionskakan som `Secure`, och utan
`Secure` bakom en TLS-proxy fungerar inloggningen ändå — men den är sämre
skyddad. Kör med TLS.

### Att hämta bilden

Paketet är privat, så servern behöver logga in en gång:

```bash
echo $GITHUB_TOKEN | docker login ghcr.io -u <användarnamn> --password-stdin
docker compose pull
```

Token behöver bara `read:packages`.

## Ordningen på överlämningen

Två system som båda äger samma grupper kommer att slåss om dem. Det gäller
både den gamla maskinen och Apps Script-et.

1. **Stoppa registret på den gamla maskinen.** `docker compose stop members`
2. **Kopiera** enligt ovan.
3. **Starta på servern.** `docker compose up -d`
4. **Kontrollera loggen.** `every synchronisation target answered` betyder att
   allt går att nå. Logga in och titta på **/synk**.
5. **Först nu: stäng av Apps Script-triggern.** Fram till dess håller den
   grupperna rätt om något går fel i steg 3.
6. **Ta bort registret från den gamla maskinen**, så att ingen råkar starta
   det igen: `docker compose down -v`. Det `-v` raderar volymen — gör det bara
   när servern har verifierats.

## Att det blev rätt

- Loggen säger `every synchronisation target answered`.
- **/synk** visar sex mål och inget rött.
- **/** visar 114 medlemmar.
- **/logg** har historiken kvar från importen — det är beviset på att
  databasen följde med och inte gjordes om.
- Inloggning fungerar för alla tre kontona.

## Säkerhetskopiering, när det väl står där

Allt ligger i en fil, och det är medlemsuppgifter. Ett schemalagt jobb:

```bash
docker compose stop members
docker run --rm -v members_members-data:/d -v /backup:/b alpine \
    tar czf /b/members-$(date +%F).tgz -C /d .
docker compose start members
```

Att stoppa i några sekunder är priset för en kopia man vet är hel. Vill man
inte det, ta med `members.db-wal` — en kopia av bara `members.db` från en
igångvarande databas saknar det senaste som skrivits.

Google är ingen säkerhetskopia. Grupperna har adresserna, kontakterna har namn
och nummer, men inträdesdatum, betalningar och loggen finns bara här.
