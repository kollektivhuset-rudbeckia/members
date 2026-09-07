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
BASE_URL=https://members.rudbeckia.nu    # inte localhost
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
members.rudbeckia.nu {
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

---

## Automatisk utrullning

Efter den första handpåläggningen sköter sig servern själv. En push till
`main` bygger en image; när bygget lyckats kör **Deploy to quebec** och
servern hämtar den nya imagen och startar om.

Kedjan hänger på `workflow_run` och inte på pushen, så en utrullning kan
aldrig hinna före bygget och starta om på gårdagens image. Ett bygge som
misslyckas rullas inte ut alls.

### Hur den kommer in

Nyckeln i `QUEBEC_SSH_KEY` når kontot `deploy` på quebec, och med den nyckeln
kan kontot göra exakt en sak. `authorized_keys` binder nyckeln till ett
*forced command*:

```
command="/usr/local/bin/deploy",no-agent-forwarding,no-port-forwarding,no-pty,...
```

Vad den andra änden än ber om kör ssh det skriptet. Inget skal, ingen scp,
ingen vidarebefordran. Kontot har inget lösenord och ligger inte i `sudo`.

Skriptet ägs av root och går inte att skriva till från `deploy`, så nyckeln
kan inte heller peka om sig själv.

Ett forced command hänger på *nyckeln*, inte på repot. Eftersom nyckeln är
gemensam för husets tre tjänster kan den alltså inte i sig säga vilken som ska
startas om, och därför skickar workflowet namnet som ssh-kommando — `ssh
"$USER@$HOST" members`. Det körs inte som ett kommando: `sshd` lägger strängen
i `SSH_ORIGINAL_COMMAND` och kör skriptet ändå, och skriptet matchar den mot
en fast lista där varje gren sätter katalog, container och port från
literaler. Allt annat avvisas i stället för att gissas på.

Baksidan av en gemensam nyckel är värd att säga rakt ut: varje repo i
organisationen som kommer åt hemligheten kan rulla ut vilken som helst av de
tre tjänsterna. Vill man inte det, är det nyckeln som ska delas upp — en per
tjänst, var och en bunden till sitt eget skript. Skriptet står i sin helhet i
[bokningens motsvarande
sida](https://github.com/kollektivhuset-rudbeckia/booking/blob/main/docs/deploy.md#skriptet-på-servern).

### Vad som skickas

Deploy-nycklar är avstängda för repot, så i stället för att ge servern en
GitHub-kredential den annars aldrig behöver tar utrullningen med sig det den
ska ha. Workflowet packar tre filer och skickar dem över samma ssh-kanal:

| Fil | Varför |
|---|---|
| `config.yaml` | föreningens uppgifter, som containern läser från disk |
| `docker-compose.yml` | hur den körs |
| `DEPLOYED_SHA` | vilken commit som rullades ut |

Skriptet packar upp **bara** de tre — de står uppräknade vid namn, vilket är
det som gör att ett arkiv inte kan skriva var det vill. `.env` och
`secrets/` finns bara på servern och rörs aldrig av en utrullning.

Servern har alltså ingen GitHub-token och ingen väg till GitHub alls.

### Hemligheter i organisationen

De fyra ligger på **organisationen** och inte på repot, så att nästa app som
ska rullas ut till quebec kan använda samma uppsättning utan att någon behöver
klistra in en nyckel igen. Synligheten är *all repositories*.

Ett secret på repot **skuggar** ett med samma namn på organisationen. Det är
tyst — utrullningen blir grön och använder repots värde ändå. Ligger det ett
kvar på ett repo är det därför värt att ta bort det, inte att låta ligga:

```bash
gh secret list --repo kollektivhuset-rudbeckia/<repo>
```

| Secret | Vad |
|---|---|
| `QUEBEC_SSH_KEY` | privata halvan av nyckeln som kör `/usr/local/bin/deploy` |
| `QUEBEC_HOST` | `ssh.rudbeckia.nu` |
| `QUEBEC_USER` | `deploy` |
| `QUEBEC_KNOWN_HOSTS` | värdnyckeln, så att utrullningen inte litar på vad som helst |

### När något går fel

Utrullningen väntar på att `/healthz` svarar och avbryter med de sista
loggraderna om den aldrig gör det. Den rullar **inte** tillbaka av sig själv —
en container som inte startar lämnar den förra imagen kvar i registret, och
att välja version är ett beslut för en människa:

```bash
ssh ssh.rudbeckia.nu
cd /srv/members
docker compose down
docker run --rm -v members_members-data:/d -v /backup:/b alpine \
    tar xzf /b/members-YYYY-MM-DD.tgz -C /d      # bara om databasen är trasig
docker compose up -d
```

Vill du rulla ut för hand, eller igen efter en misslyckad körning:
**Actions → Deploy to quebec → Run workflow**.

Att en synksignal saknas är ingen misslyckad utrullning. Registret fungerar
utan Google, och skriptet skriver `NOTE: a synchronisation target did not
answer` i stället för att fälla körningen. Vad som är fel står på **/synk**.
