# Koppla registret till Google Workspace

Steg för steg, för `rudbeckia.nu`. Räkna med en halvtimme första gången.

Registret behöver två helt olika saker av Google, och de blandas lätt ihop:

| | Vad den gör | Utan den |
|---|---|---|
| **OAuth-klient** | låter en människa logga in med sitt Google-konto | ingen kommer in — registret startar inte |
| **Tjänstekonto** | låter servern skriva till grupper, kontakter och kalkylark *utan* att någon är inloggad | registret fungerar, men ingenting synkroniseras och varje sida säger det med en gul banner |

Du kan göra den första nu och den andra nästa helg. Registret är fullt
användbart däremellan.

**Du behöver:** ett konto som är **superadministratör** i Workspace för
`rudbeckia.nu`, och rätt att skapa projekt i Google Cloud. Samma person brukar
ha båda.

> Google flyttar runt i sina konsoler ett par gånger om året. Menyvägarna
> nedan stämde när det här skrevs; hittar du inte en sida, sök på dess namn i
> konsolens sökruta så brukar den dyka upp.

---

## Innan du börjar: kontrollera att de tre kontona är riktiga konton

Det här är det enda steget som kan tvinga fram ett omtag längre ner, så ta det
först.

`ny@`, `ekonomi@` och `styrelsen@rudbeckia.nu` måste vara **användarkonton**,
inte grupper eller alias. Registret behöver två saker av dem som en grupp inte
kan ge:

- de ska kunna **logga in** — bara ett användarkonto har ett lösenord
- de ska ha en **egen adressbok** — bara ett användarkonto har kontakter

Kontrollera i [admin.google.com](https://admin.google.com/) → **Katalog →
Användare**. Står de i stället under **Katalog → Grupper** är de alias, och då
behöver ni göra om dem till användarkonton (vilket kostar en licens styck)
innan kontaktspeglingen och inloggningen fungerar.

Behöver ni bara komma igång kan ni börja med ett enda riktigt konto,
`styrelsen@`, och lägga till de andra sedan:

```bash
ACCOUNT_INTAKE=styrelsen@rudbeckia.nu
ACCOUNT_CASHIER=styrelsen@rudbeckia.nu
ACCOUNT_BOARD=styrelsen@rudbeckia.nu
```

Då gör alla allt, vilket är sämre men fungerar. I `config.yaml` tar du bort de
konton ur `contacts.accounts` som inte finns ännu.

---

## Del 1 — Google Cloud-projektet

1. Gå till [console.cloud.google.com](https://console.cloud.google.com/).
2. Kontrollera **överst i sidhuvudet** att du är i organisationen
   `rudbeckia.nu` och inte i "Inget företag". Ligger projektet utanför
   organisationen går domänvid delegering inte att koppla ihop med det senare.
3. **Välj projekt → Nytt projekt**. Namn: `rudbeckia-members`. Plats:
   organisationen `rudbeckia.nu`.
4. Skapa, och välj det nya projektet.

## Del 2 — Slå på de tre API:erna

**APIs & Services → Library**, sök upp och tryck **Enable** på var och en:

| API | Vad registret gör med det |
|---|---|
| **Admin SDK API** | lägger till och tar bort adresser i `bomedlemmar@` och `friends@` |
| **People API** | håller de tre kontonas adressböcker i takt |
| **Google Sheets API** | skriver fliken kassörerna räknar i |

Hoppar du över ett av dem får du ett `403 ... has not been used in project
...` som säger precis vilket, med en länk som slår på det.

## Del 3 — OAuth-klienten (inloggningen)

### 3a. Samtyckesskärmen

**APIs & Services → OAuth consent screen** (i nyare konsoler: **Google Auth
Platform → Branding**).

| Fält | Värde |
|---|---|
| User type | **Internal** |
| App name | `Rudbeckia medlemsregister` |
| User support email | `styrelsen@rudbeckia.nu` |
| Developer contact | din egen adress |

**Internal** är viktigt. Det betyder att bara konton i `rudbeckia.nu` kan
logga in över huvud taget, och att appen slipper Googles granskning — den kan
annars ta veckor. Alternativet **External** skulle innebära att vem som helst
med ett Google-konto når inloggningssidan, och då är kontolistan i `.env` det
enda som står emellan.

Scopes behöver du inte lägga till här. Registret ber bara om `openid`, `email`
och `profile`, som är förvalda.

### 3b. Klienten

**APIs & Services → Credentials → Create credentials → OAuth client ID**.

| Fält | Värde |
|---|---|
| Application type | **Web application** |
| Name | `members` |
| Authorized JavaScript origins | *(lämna tom — registret kör ingen JavaScript mot Google)* |
| Authorized redirect URIs | `https://medlemmar.rudbeckia.nu/oauth2/callback` |

Lägg till `http://localhost:8080/oauth2/callback` också om du vill kunna köra
skarpt läge lokalt.

**Adressen måste stämma tecken för tecken** med `BASE_URL` + `/oauth2/callback`.
Ingen avslutande snedstreck, rätt protokoll, rätt värdnamn. Det här är det
absolut vanligaste felet, och det syns som `redirect_uri_mismatch` i
webbläsaren med den förväntade adressen utskriven — jämför den med det som
står i konsolen.

Kopiera **Client ID** och **Client secret** till `.env`:

```bash
GOOGLE_CLIENT_ID=1234567890-abc....apps.googleusercontent.com
GOOGLE_CLIENT_SECRET=GOCSPX-....
BASE_URL=https://medlemmar.rudbeckia.nu
GOOGLE_HOSTED_DOMAIN=rudbeckia.nu
```

Nu kan folk logga in. Starta gärna om och prova innan du går vidare — det är
lättare att felsöka en sak i taget.

## Del 4 — Tjänstekontot (synkroniseringen)

### 4a. Skapa kontot

**IAM & Admin → Service Accounts → Create service account**.

| Fält | Värde |
|---|---|
| Name | `members-sync` |
| Description | `Synkroniserar medlemsregistret mot grupper, kontakter och kalkylark` |
| Grant this service account access to project | **hoppa över** |
| Grant users access to this service account | **hoppa över** |

Kontot ska medvetet **inte** ha några IAM-roller. Allt det gör sker genom
domänvid delegering i Workspace, inte genom rättigheter i Cloud-projektet. En
roll här skulle ge det åtkomst till projektet utan att hjälpa mot Workspace.

### 4b. Nyckeln

Öppna kontot → **Keys → Add key → Create new key → JSON → Create**.

Filen laddas ner **en gång**. Går den förlorad kan du inte hämta den igen, bara
göra en ny.

```bash
mkdir -p secrets
mv ~/Hämtningar/rudbeckia-members-*.json secrets/service-account.json
chmod 600 secrets/service-account.json
```

`secrets/` ligger i `.gitignore`. Kontrollera gärna:

```bash
git check-ignore -v secrets/service-account.json   # ska skriva ut en rad
```

Nyckeln är ett lösenord som kan skriva i hela domänens grupper och
adressböcker. Den ska ligga på servern och ingen annanstans — inte i ett
chattmeddelande, inte i repot.

### 4c. Kontots Unique ID

På tjänstekontots sida står **Unique ID**, ett tjugosiffrigt tal. Kopiera det —
det är den enda uppgiften nästa steg behöver.

> Det är *inte* samma sak som `client_email` i JSON-filen. Delegeringsrutan
> vill ha siffran.

## Del 5 — Domänvid delegering

Det här är steget som gör att tjänstekontot får agera som en människa. Det
sker i admin-konsolen, inte i Cloud.

[admin.google.com](https://admin.google.com/) → **Säkerhet → Åtkomst- och
datakontroll → API-kontroller** → längst ner, **Hantera domänvid delegering**
→ **Lägg till ny**.

| Fält | Värde |
|---|---|
| Client ID | tjugosiffran från 4c |
| OAuth-omfattningar | de fyra nedan |

### Varje scope måste vara hela adressen

Det här är den enda fällan i hela uppsättningen som ser ut som ett stavfel och
inte är det. Rutan kräver **hela URL:en**, inklusive
`https://www.googleapis.com/auth/`. Skriver du bara den sista biten —

```
admin.directory.group.member          ← Ogiltig omfattning
```

— svarar rutan **”Ogiltig omfattning”** i rött och vägrar spara. Det gäller
alla fyra, även `contacts` och `spreadsheets` som ser korta nog att klara sig
utan.

Rätt form:

```
https://www.googleapis.com/auth/admin.directory.group.member
https://www.googleapis.com/auth/admin.directory.group.readonly
https://www.googleapis.com/auth/contacts
https://www.googleapis.com/auth/spreadsheets
```

Enklast: kopiera raden nedan och klistra in **hela** i den *första* rutan.
Dialogen delar själv upp den på kommatecknen och lägger en rad per scope, så
du slipper skriva fyra långa adresser för hand.

```
https://www.googleapis.com/auth/admin.directory.group.member,https://www.googleapis.com/auth/admin.directory.group.readonly,https://www.googleapis.com/auth/contacts,https://www.googleapis.com/auth/spreadsheets
```

**Ett scope som skiljer sig på ett tecken ger ett `unauthorized_client` som
inte säger vilket** — så jämför hellre en gång för mycket. Samma lista finns i
koden, i `internal/google/token.go`.

Vad de fyra får göra, och inte:

| Scope | Får | Får inte |
|---|---|---|
| `admin.directory.group.member` | lägga till och ta bort adresser i grupper | skapa användare, ändra lösenord, röra andra grupper än de den ombeds |
| `admin.directory.group.readonly` | läsa att en grupp finns | ändra någonting |
| `contacts` | läsa och skriva adressböckerna hos de konton den agerar som | röra ett konto den inte agerar som |
| `spreadsheets` | läsa och skriva kalkylark som kontot den agerar som kommer åt | öppna Drive i övrigt, läsa dokument |

Ingen av dem ger tillgång till post. Registret kan inte läsa någons e-post.

### 5b. Administratören att agera som

Att ändra en grupp är en administratörsåtgärd. Ett tjänstekonto kan inte göra
det som sig självt, hur många scopes det än har — det måste agera som en
människa som har rätten. Peka ut vem:

```bash
GOOGLE_ADMIN_SUBJECT=admin@rudbeckia.nu
```

Kontot måste finnas och ha **superadministratör**, eller åtminstone
administratörsrollen **Grupper**. Ett vanligt konto ger `403 Not Authorized to
access this resource/api` när registret försöker läsa en grupp.

Kontaktspeglingen agerar i stället som vart och ett av de tre kontona, och
kalkylarket som `styrelsen@`. Det behöver du inte konfigurera — registret tar
adresserna ur `contacts.accounts` och `ACCOUNT_BOARD`.

## Del 6 — Grupperna

De finns redan, men kontrollera två saker i **Katalog → Grupper**:

1. Adresserna är exakt `bomedlemmar@rudbeckia.nu` och `friends@rudbeckia.nu`,
   som i `config.yaml`.
2. Under gruppens **Inställningar → Medlemmar** står det vem som får läggas
   till. Är den satt till att bara medlemmar i organisationen får vara med
   kommer externa adresser — och de flesta medlemmar har gmail eller hotmail —
   att avvisas med `403`. Den ska tillåta medlemmar utanför organisationen.

Registret rör aldrig gruppens ägare eller managers, bara vanliga medlemmar.

## Del 7 — Kalkylarket

1. Skapa ett nytt kalkylark **som `styrelsen@rudbeckia.nu`**, eller dela det
   med den adressen som **redigerare**. Det är det kontot registret skriver
   som.
2. Döp den första fliken till `Medlemmar`.
3. Kopiera id:t ur adressen och lägg det i `config.yaml`:

   ```
   docs.google.com/spreadsheets/d/1AbC...XyZ/edit
                                  ^^^^^^^^^^ det här
   ```

   ```yaml
   sheet:
     id: "1AbC...XyZ"
     tab: Medlemmar
   ```

Fliken **ägs av registret och skrivs om vid varje körning**. Lägg era egna
formler på en annan flik som läser från den här, annars försvinner de inom tio
minuter.

Vill ni inte ha kalkylarket ännu: lämna `sheet.id` tom, så hoppas det över.

## Del 8 — Starta och kontrollera

```bash
docker compose up -d
docker compose logs -f
```

Så här ser det ut när allt stämmer:

```
level=INFO msg="configuration loaded" targets="[group:bomedlemmar@rudbeckia.nu ...]"
level=INFO msg=account role=intake email=ny@rudbeckia.nu may_record_payments=false
level=INFO msg=account role=cashier email=ekonomi@rudbeckia.nu may_record_payments=true
level=INFO msg=account role=board email=styrelsen@rudbeckia.nu may_record_payments=false
level=INFO msg="every synchronisation target answered" targets=[...]
level=INFO msg="synchronisation is on" every=10m0s
level=INFO msg=listening addr=:8080
```

Raden **`every synchronisation target answered`** är den som betyder att
uppsättningen ovan gick hem. Saknas den står det i stället en rad per mål som
inte gick att nå, med en mening om varför. Servern startar ändå, med flit: ett
register som vägrar starta för att Google har en dålig morgon är sämre än ett
som startar och säger till.

Logga sedan in och gå till **/synk**. Där står varje mål, hur många adresser
som stämmer, och vad Google svarade om något inte gjorde det.

---

## När det inte fungerar

Registret skriver ut Googles egna felmeddelanden ordagrant, på **/synk** och i
loggen. Här är vad de brukar betyda.

### Inloggningen

| Vad du ser | Vad det är | Vad du gör |
|---|---|---|
| `redirect_uri_mismatch` | adressen i konsolen är inte `BASE_URL` + `/oauth2/callback` | jämför tecken för tecken; felmeddelandet visar den förväntade |
| `Det kontot får inte använda registret` | inloggningen gick bra, men adressen står inte i `ACCOUNT_*` | lägg till den, eller logga in som rätt konto |
| `Bara konton i rudbeckia.nu får logga in` | ett privat Google-konto, eller `GOOGLE_HOSTED_DOMAIN` fel | logga in som föreningens konto |
| `access_denied` direkt av Google | samtyckesskärmen är **Internal** och kontot ligger utanför organisationen | rätt konto, eller kontrollera att projektet ligger i organisationen |
| Sidan kommer tillbaka till inloggningen om och om igen | kakan går förlorad — `BASE_URL` är `https://` men sajten nås över `http://` | rätta `BASE_URL`, eller sätt TLS framför |

### Synkroniseringen

| Vad du ser | Vad det är | Vad du gör |
|---|---|---|
| **`Ogiltig omfattning`** i delegeringsrutan | ett scope saknar `https://www.googleapis.com/auth/` | skriv hela adressen, se Del 5 |
| `Google will not let the service account act as ...` (`unauthorized_client`) | domänvid delegering saknas, eller ett scope skiljer sig | kontrollera Unique ID och de fyra scopes i Del 5 |
| `Google refused the impersonation of ...` (`invalid_grant`) | kontot finns inte, eller serverns klocka går fel | kontrollera adressen; kör `timedatectl` på servern |
| `the service account ... may not read ...` | `GOOGLE_ADMIN_SUBJECT` är inte administratör | ge kontot rollen Grupper, eller peka ut en superadmin |
| `there is no group ... in the Workspace` | felstavad adress i `config.yaml`, eller gruppen finns inte | rätta adressen |
| `403` när en extern adress läggs till i en grupp | gruppen tillåter inte medlemmar utanför organisationen | ändra gruppens medlemsinställning, se Del 6 |
| `there is no spreadsheet with id ...` | arket är inte delat med `styrelsen@` | dela som redigerare |
| `the spreadsheet has no tab called "Medlemmar"` | fliken heter något annat | döp om fliken, eller rätta `sheet.tab` |
| `has not been used in project ... before or it is disabled` | ett API är inte påslaget | följ länken i felmeddelandet, se Del 2 |
| `the label ... holds N contacts, more than the 1000 Google will list` | någon har lagt tusentals kort under etiketten | rensa för hand innan registret rör den |

### En enskild adress vill inte synkroniseras

Står en enda medlem röd på **/synk** medan resten är gröna är det nästan
alltid adressen själv: felstavad, eller en domän som avvisar. Meddelandet
bredvid är Googles eget. Rätta adressen på medlemmens sida, så försöker
registret igen inom fem sekunder.

Är adressen en **gmail-adress som ser rätt ut** och ändå gnäller om att den
redan finns — kontrollera att `gmail.com` står kvar i `sync.dot_fold_domains`
i `config.yaml`. Utan den behandlas `anna.andersson@` och `annaandersson@` som
två olika brevlådor, och registret försöker lägga till en adress Google redan
har, om och om igen.

---

## Underhåll

### Byta nyckel

Nycklar bör bytas när någon som haft tillgång till servern slutar, och annars
med några års mellanrum.

1. Tjänstekontot → **Keys → Add key** → ny JSON-nyckel.
2. Lägg den nya filen på servern, `docker compose up -d`.
3. Kontrollera `every synchronisation target answered` i loggen.
4. Först då: ta bort den gamla nyckeln i konsolen.

Delegeringen i Del 5 sitter på kontot, inte på nyckeln, så den behöver inte
göras om.

### Stänga av synkroniseringen

Ta bort `GOOGLE_SERVICE_ACCOUNT_FILE` ur `.env` och starta om. Registret
fortsätter precis som vanligt och säger på varje sida att ingenting
synkroniseras. Grupperna blir kvar som de är.

### Ta bort registrets åtkomst helt

1. Radera raden i **Hantera domänvid delegering**. Från och med då kan
   nyckeln ingenting, även om någon har den.
2. Radera tjänstekontot.
3. Radera OAuth-klienten, så att ingen kan logga in.

### Om ni börjar om från början

Kör `-import -dry-run` igen. Importen ändrar aldrig någon som redan finns och
kan köras hur många gånger som helst — se avsnittet *Migrera från
Apps Script-et* i [README](../README.md).

---

## Vad registret faktiskt kan

Värt att kunna svara på när någon på husmötet frågar.

**Det kan:** lägga till och ta bort adresser i `bomedlemmar@` och `friends@`;
läsa och skriva kontaktkorten under etiketterna `Bomedlemmar` och
`Vänmedlemmar` i tre adressböcker; skriva en flik i ett kalkylark.

**Det kan inte:** läsa någons e-post; skapa, ändra eller ta bort användare;
ändra lösenord; röra andra grupper än de två; röra kontakter utanför sina egna
etiketter; öppna Drive i övrigt.

**Vad det sparar om medlemmarna:** namn, e-post, telefon, lägenhet,
inträdesdatum, en anteckning, och vilka år avgiften betalats. Allt ligger i en
enda SQLite-fil på er egen server. Ingenting skickas någon annanstans än till
Google, och dit bara det som redan står i grupperna.
