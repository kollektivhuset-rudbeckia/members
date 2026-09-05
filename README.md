# members

Medlemsregister för Kollektivhuset Rudbeckia. Styrelsen, kassörerna och den
som svarar på "jag skulle vilja gå med" delar ett register som håller Googles
grupper, adressböcker och kassörernas kalkylark i takt med sig själv — och som
säger tydligt ifrån när något inte kommer fram.

Byggt som en enda statisk Go-binär med SQLite. Ingen databasserver, ingen
byggkedja för frontend, inga beroenden utöver SQLite-drivrutinen och en
YAML-läsare. Sidan finns på svenska och engelska.

---

## Prova på

Vill du bara se hur det ser ut? Starta en demo. Den behöver ingen
konfiguration, inga Google-konton och ingen databas — och den fyller sig själv
med en påhittad förening så att du har något att klicka på.

**Med Docker** — det enda du behöver är Docker:

```bash
docker run --rm -p 8080:8080 -e DEMO=true \
    ghcr.io/kollektivhuset-rudbeckia/members:latest
```

**Med Go** — om du klonat repot:

```bash
make demo          # eller: go run ./cmd/server -demo
```

**Med repot och Docker** — bygger från källkoden om bilden inte går att hämta:

```bash
docker compose -f docker-compose.demo.yml up
```

Öppna sedan <http://localhost:8080> och välj vilken roll du vill prova.
Demoläget har ingen inloggning alls: du klickar på **Ny**, **Ekonomi** eller
**Styrelsen** och är det. Trettio påhittade medlemmar, några förfallna
avgifter, ett par tidigare medlemmar och två förslag som väntar på styrelsen.

Saker att testa: lägg till en medlem som **Ny** och se att ändringen av någon
befintlig i stället blir ett förslag; godkänn det som **Styrelsen**; pricka av
en avgift som **Ekonomi** och märk att varken Ny eller Styrelsen kan det;
sortera registret på *Tid som medlem*; filtrera på *Förfallen*.

Demon säger tydligt ifrån med en banner på varje sida, all data ligger i
minnet och ingenting når Google. Kör aldrig `DEMO=true` skarpt — vem som helst
kan välja att vara styrelsen.

---

## Kom igång på riktigt

Registret behöver två saker av Google: en **OAuth-klient** för att logga in
folk, och ett **tjänstekonto** för att skriva till grupper, kontakter och
kalkylark. Det första krävs, det andra kan vänta — utan tjänstekonto fungerar
registret precis som vanligt, men ingenting synkroniseras, och varje sida
säger det med en gul banner.

> **Sätter du upp det för första gången?** Ta
> [docs/google-workspace.md](docs/google-workspace.md) i stället. Det är samma
> sak steg för steg, med skärmvägar, vad de fyra behörigheterna får och inte
> får göra, och en felsökningstabell över vad Googles meddelanden betyder.
> Sammanfattningen nedan är för den som redan vet ungefär vad som ska hända.

### 1. OAuth-klienten (inloggningen)

I [Google Cloud Console](https://console.cloud.google.com/), i ett projekt som
hör till organisationen:

1. **APIs & Services → OAuth consent screen**. Välj **Internal** — bara
   föreningens egna konton ska in — och fyll i namn och supportadress.
2. **APIs & Services → Credentials → Create credentials → OAuth client ID**.
   Typ: **Web application**.
3. Under **Authorized redirect URIs**, lägg till exakt:

   ```
   https://members.rudbeckia.nu/oauth2/callback
   ```

   Adressen måste vara `BASE_URL` + `/oauth2/callback`, tecken för tecken.
   Kör du lokalt lägger du till `http://localhost:8080/oauth2/callback` också.
4. Kopiera **Client ID** och **Client secret** till `.env`.

### 2. Tjänstekontot (synkroniseringen)

Det här är den del som brukar ta en kvart och kännas som en timme, så här är
hela listan.

1. **IAM & Admin → Service Accounts → Create service account**. Kalla det
   `members-sync`. Det behöver *inga* IAM-roller i projektet — allt det gör
   sker med domänvid delegering i stället.
2. På kontot: **Keys → Add key → Create new key → JSON**. Filen laddas ner en
   gång. Lägg den i `secrets/service-account.json` (mappen är i
   `.gitignore`).
3. Notera kontots **Unique ID** (en lång siffra, kallas ibland Client ID).
4. Aktivera de API:er registret använder, i **APIs & Services → Library**:
   **Admin SDK API**, **People API** och **Google Sheets API**.
5. I [Google Workspace admin-konsolen](https://admin.google.com/):
   **Säkerhet → Åtkomst- och datakontroll → API-kontroller → Hantera
   domänvid delegering → Lägg till ny**. Klistra in kontots Unique ID och
   exakt de här scopes, kommaseparerade:

   ```
   https://www.googleapis.com/auth/admin.directory.group.member,
   https://www.googleapis.com/auth/admin.directory.group.readonly,
   https://www.googleapis.com/auth/contacts,
   https://www.googleapis.com/auth/spreadsheets
   ```

   Det är de smalaste som gör jobbet. De kan lägga till och ta bort medlemmar
   i grupper, läsa och skriva de tre kontonas adressböcker och skriva
   kalkylarket. De kan inte skapa användare, läsa post eller se ett lösenord.
   Ett scope som står i koden men inte här ger ett förvirrande 401 vid
   körning, så det är värt att jämföra tecken för tecken.
6. Sätt `GOOGLE_ADMIN_SUBJECT` till en Workspace-administratör, t.ex.
   `admin@rudbeckia.nu`. Att ändra en grupp är en administratörsåtgärd — ett
   tjänstekonto kan inte göra det som sig självt, hur många scopes det än har,
   utan agerar som den här personen.

### 3. Vad som ska finnas i Workspace

Registret skapar kontaktetiketterna själv om de saknas, men grupperna och
kalkylarket måste finnas:

| Sak | Vad | Skapas av |
|---|---|---|
| Google-grupp | `bomedlemmar@rudbeckia.nu` | finns redan |
| Google-grupp | `friends@rudbeckia.nu` | finns redan |
| Konton som loggar in | `ny@`, `ekonomi@`, `styrelsen@rudbeckia.nu` | finns redan |
| Kontaktetikett | `Bomedlemmar` i varje kontos adressbok | registret, om den saknas |
| Kontaktetikett | `Vänmedlemmar` i varje kontos adressbok | registret, om den saknas |
| Kalkylark | ett ark med en flik som heter `Medlemmar` | **du**, en gång |

Kalkylarket: skapa ett nytt ark, döp fliken till `Medlemmar`, dela det med
`styrelsen@rudbeckia.nu` som redigerare, och klistra in id:t ur adressen i
`config.yaml`:

```
docs.google.com/spreadsheets/d/DET_HÄR_ÄR_ID:T/edit
```

Lämna `sheet.id` tomt så länge, om ni inte vill ha kalkylarket ännu.

### 4. Starta

```bash
cp .env.example .env
$EDITOR .env                 # åtminstone GOOGLE_CLIENT_ID och _SECRET
mkdir -p secrets && cp ~/Downloads/members-sync-*.json secrets/service-account.json
docker compose up -d
docker compose logs -f
```

Loggen säger `every synchronisation target answered` om allt går att nå, och
skriver ut varje mål som inte gör det med en mening om vad som är fel. Den
stoppar aldrig servern för det: ett register som vägrar starta för att Google
har en dålig morgon är sämre än ett som startar och säger till.

---

## Migrera från Apps Script-et

Idag är sanningen en **kontaktetikett hos `styrelsen@rudbeckia.nu`**, som ett
Apps Script kopierar till de två grupperna. Registret tar över den rollen, så
det första det behöver är just den listan.

Kör alltid en repetition först. Den skriver ingenting:

```bash
docker compose run --rm members -import -dry-run
# eller, lokalt:  make import-dry
```

Du får tre listor och en varning:

```
  Kan importeras — 41 st.
    Anna Andersson              anna.andersson@example.com          bo
    ...
  Finns redan i registret — 0 st.
  Behöver rättas för hand — 2 st.
    Bo Bengtsson                                                    bo  ← kontakten har ingen e-postadress
                                cecilia@example.com                 van ← kontakten har inget namn

  I en Google-grupp men under ingen etikett — 7 st.
  De här försvinner ur gruppen vid första synken om de inte
  läggs in i registret eller i groups[].keep i config.yaml:
    nagon@example.com (bomedlemmar@rudbeckia.nu)
    ...
```

Den sista listan är den viktiga. Det är personerna som lagts direkt i gruppen
utan att stå i kontaktregistret — matlagsledarna, framför allt. Registret
äger grupperna och rensar det som inte står i det, så de skulle försvinna vid
första körningen. `config.yaml` har dem redan i `groups[].keep`, men gå
igenom listan: den som egentligen *är* medlem ska skrivas in som medlem, och
då kan raden tas bort ur `keep`.

När listorna ser rimliga ut, importera:

```bash
docker compose run --rm members -import -import-joined=2026-01-01
```

`-import-joined` krävs och är ett medvetet val. Ett kontaktkort säger inte
när någon gick med, och det finns inget att gissa utifrån — en gissning skulle
lägga ett felaktigt tal i just den kolumn registret finns för. Använd
föreningens startdatum om ni vill räkna tiden därifrån, eller dagens datum om
ni hellre börjar räkna om från migreringen. Varje importerad medlem får en
anteckning som säger att tiden kommer från importen.

Importen ändrar aldrig någon som redan finns i registret, skriver ingenting
till Google, och kan köras om: den hoppar över det som redan är på plats.

**Efter importen:** `chase_from_year` behöver ingen inställning. Registret
jagar aldrig en avgift för ett år innan medlemmens rad skapades, så en
importerad medlem som gick med 2014 får inte elva års skuld på halsen. Vill ni
tvärtom ha hela historiken jagad, sätt `membership.chase_from_year` till året
ni har böcker från.

När registret rullar: **stäng av Apps Script-triggern.** Två system som båda
äger samma grupper kommer att slåss om den.

---

## Tre roller, tre brevlådor

Föreningen har redan tre delade brevlådor med tre olika jobb, så registret
lånar dem i stället för att uppfinna en fjärde sak att administrera. Vem som
kommer in avgörs av `ACCOUNT_*` i `.env`, och ingenting utanför de listorna
släpps in — oavsett Google-domän.

|  | `ny@` | `ekonomi@` | `styrelsen@` |
|---|:---:|:---:|:---:|
| Se registret | ✓ | ✓ | ✓ |
| Lägga till en medlem | ✓ | ✓ | ✓ |
| Ändra en befintlig | förslag | ✓ | ✓ |
| Ta bort någon | förslag | förslag | ✓ |
| Pricka av en avgift | – | ✓ | – |
| Ta ställning till förslag | – | – | ✓ |
| Synkronisera för hand | – | – | ✓ |
| Läsa loggen | – | – | ✓ |

**Att lägga till är fritt för alla.** Det är hela poängen: en medlem skrivs in
i samma stund hen visar intresse och hamnar i sin Google-grupp inom en minut,
så att hen börjar få husets utskick direkt. Avgiften kommer när den kommer.

**Att ändra eller ta bort någon som redan finns går via styrelsen** för den
som inte får göra det själv. Förslaget hamnar under **Ändringar**, med en
tabell som visar exakt vilka fält som skulle bli vad. Styrelsen godkänner
eller avslår, och båda hamnar i loggen.

Två saker som är värda att veta om förslag:

- En medlem kan bara ha **ett** väntande förslag i taget. Två förslag ovanpå
  varandra skulle betyda att styrelsen väljer mellan två versioner som ingen
  kan rekonstruera.
- Om någon annan ändrar medlemmen medan förslaget väntar blir förslaget
  **inaktuellt** i stället för godkännbart. Att godkänna det skulle tysta
  skriva över deras arbete.

**Bara ekonomi prickar av avgifter.** De är de som har bankkontot framför sig,
och en betalning som någon annan bockat av är ett tal ingen kan gå i god för.
Vill styrelsen kunna rätta en felprickning: `PAYMENT_ROLES=cashier,board`.

---

## Avgifter

Årsavgiften löper per kalenderår: en betalning täcker januari till december.
Kassören prickar av mot bankkontot, år för år, på medlemmens sida eller — för
tio i rad utan att lämna sidan — på **Avgifter**.

Vad som räknas som förfallet står i `config.yaml`:

```yaml
membership:
  fee_kr: 200
  due_on: "03-31"        # sista dagen att betala
  grace_days: 14         # hur länge efter det någon får vara i fred
  new_member_days: 45    # hur länge en ny medlem får på sig, från sin egen dag
```

Den sista raden är den som gör att man kan skriva in någon direkt. Sista dagen
för en medlem är den *senare* av föreningens datum plus respit, och medlemmens
egen frist räknad från dagen registret fick veta om hen. Någon som skrivs in
den 20 november har inte missat ett datum i mars — hen är röd först i januari.
Och en medlem som importeras i juni får sina 45 dagar från importen, inte från
2014.

Registret jagar inte bara innevarande år. Det tittar på varje år från det
tidigaste det kan svara för — aldrig före medlemmens inträde, aldrig före
raden skapades, och som standard inte längre tillbaka än förra året. Det förra
är vad som gör att en novembermedlem inte faller mellan stolarna vid årsskiftet;
det senare är vad som gör att en import inte hittar på ett decennium av skuld.

`0 kr` prickat är inte samma sak som tomt: det första betyder att avgiften
efterskänkts, det andra att ingenting kommit in.

---

## Synkroniseringen, och larmen

Registret bestämmer. Var tionde minut — och inom fem sekunder efter att någon
ändrat något — räknar registret ut vad varje mål *borde* innehålla, jämför med
vad det gör, och rättar skillnaden.

Målen är:

| Mål | Vad som händer |
|---|---|
| `bomedlemmar@rudbeckia.nu` | håller exakt registrets bomedlemmar |
| `friends@rudbeckia.nu` | håller exakt registrets vänmedlemmar |
| Kontakter hos `ny@`, `styrelsen@`, `ekonomi@` | ett kontaktkort per medlem, under etiketten för medlemstypen |
| Kalkylarket | fliken `Medlemmar` skrivs om helt |

Den viktiga halvan av det här är inte rättandet utan rapporteringen. En tyst
synk som gått sönder är sämre än ingen synk alls, för då har styrelsen
tillbringat två veckor med att tro att grupperna stämmer. Därför:

- Varje adress har ett **stående omdöme** i databasen, per mål.
- Ett misslyckande minns **när det började**. Det är skillnaden mellan något
  som gick sönder för fyra minuter sedan och något som ruttnat i fjorton dagar.
- Det som fortfarande är fel efter en halvtimme blir en **röd banner på varje
  sida**, med adressen utskriven — inte bara ett antal.
- Allt det som är fel just nu, och de senaste körningarna per mål, står på
  **/synk**. Med Googles egna felmeddelanden, ordagrant.

Det som ligger i en grupp men inte i registret tas bort. Tre sorters adresser
är skyddade från det: de som står i `groups[].keep`, föreningens egna
inloggningskonton, och den som Google kallar ägare eller manager i gruppen.
Registret administrerar medlemmar, inte grupper.

Kontaktspeglingen rör **bara kort inne i registrets egna etiketter**. Vad
kontots ägare i övrigt har i sin adressbok ser registret aldrig. Och den
jämför innan den skriver, så ett oförändrat register gör inga anrop alls —
det är det som gör att den kan gå var tionde minut i åratal.

### Punkter i e-postadresser

Google struntar i punkter i adressens första del på alla domäner det självt är
värd, så `anna.andersson@gmail.com` och `annaandersson@gmail.com` är samma
brevlåda. Registret jämför likadant, för annars skulle det försöka lägga till
en adress Google redan har, om och om igen, och rapportera en fullt fungerande
medlem som trasig var tionde minut — precis det falsklarm som får en styrelse
att sluta läsa larmen.

Foldningen gäller bara de domäner där det är sant (`sync.dot_fold_domains`,
plus Workspace-domänen). På hotmail.com *är* `a.b@` och `ab@` två olika
personer, och att tysta slå ihop dem vore värre än att hålla dem isär. Adressen
sparas och skickas alltid exakt som medlemmen skrev den — foldningen används
bara för att avgöra om två stavningar är samma brevlåda.

---

## Kalkylarket

Fliken som står i `sheet.tab` **ägs av registret och skrivs om vid varje
körning**. Skriv ingenting där själv; lägg era formler på en annan flik som
läser från den här.

En rad per medlem, en kolumn per år, belopp som rena tal så att `SUMMA`
fungerar utan att någon först måste plocka bort ett "kr". Datum i ISO-format,
så att Sheets känner igen dem som datum oavsett arkets språkinställning.
Längst ner, efter en blankrad, står när fliken senast skrevs — så att en
kassör som öppnar den i juni kan se om hen tittar på något färskt.

---

## Inställningar

Allt hemligt sätts som miljövariabler, aldrig i `config.yaml`. Se
[`.env.example`](.env.example) för hela listan.

| Variabel | Standard | Betyder |
|---|---|---|
| `GOOGLE_CLIENT_ID` | – | **Krävs.** OAuth-klienten som loggar in folk |
| `GOOGLE_CLIENT_SECRET` | – | **Krävs.** Samma klients hemlighet |
| `BASE_URL` | `http://localhost:8080` | Publika adressen. Styr redirect-URI och om kakan blir `Secure` |
| `GOOGLE_HOSTED_DOMAIN` | `rudbeckia.nu` | Enda Workspace-domänen som får logga in |
| `GOOGLE_SERVICE_ACCOUNT_FILE` | tom | Nyckelfilen för synkroniseringen. Tom = ingen synk |
| `GOOGLE_SERVICE_ACCOUNT_JSON` | tom | Samma nyckel som variabel, ren JSON eller base64 |
| `GOOGLE_ADMIN_SUBJECT` | – | Administratören tjänstekontot agerar som. Krävs om nyckeln finns |
| `ACCOUNT_INTAKE` | `ny@rudbeckia.nu` | Kontot som får lägga till men inte ändra |
| `ACCOUNT_CASHIER` | `ekonomi@rudbeckia.nu` | Kontot som prickar av avgifter |
| `ACCOUNT_BOARD` | `styrelsen@rudbeckia.nu` | Kontot som får allt, och godkänner förslag |
| `PAYMENT_ROLES` | `cashier` | Roller som får pricka av. `cashier,board` för båda |
| `SESSION_SECRET` | slumpas | Signerar sessionskakor. Tom = alla loggas ut vid deploy |
| `SESSION_HOURS` | `12` | Hur länge en inloggning håller |
| `CONFIG_PATH` | `/config.yaml` | Var föreningsfilen ligger |
| `DB_PATH` | `/data/members.db` | Var registret ligger |
| `LISTEN_ADDR` | `:8080` | Adress att lyssna på |
| `TRUST_PROXY` | `true` | Läs klientens IP ur `X-Forwarded-For` |
| `LOG_LEVEL` | `info` | `debug`, `info`, `warn` eller `error` |
| `DEMO` | `false` | Demoläge: påhittade medlemmar, roller på begäran, ingen Google. **Aldrig skarpt** |

Hela uppsättningen mot Google — projektet, API:erna, klienten, tjänstekontot
och den domänvida delegeringen — står i
[docs/google-workspace.md](docs/google-workspace.md), tillsammans med vad man
gör åt varje felmeddelande.

Föreningens egna uppgifter — avgiften, förfallodagen, grupperna, etiketterna,
kalkylarket, synkintervallet — står i [`config.yaml`](config.yaml), som är
kommenterad rad för rad. Kontrollera den innan du startar om:

```bash
docker compose run --rm members -check-config
```

Filen läses vid start, och en felstavad nyckel avvisas i stället för att
ignoreras — annars skulle en avgift man tror att man satt aldrig ha lästs.

---

## Två språk

Hela sajten finns på svenska och engelska. Flaggan uppe till höger byter, och
valet minns i ett år i en kaka. Har man inte valt något gäller webbläsarens
`Accept-Language`, och säger den ingenting vi förstår används `site.language`
ur `config.yaml`.

Orden ligger i `internal/i18n/catalog.go`, två språk sida vid sida på varje rad:

```go
"fee.overdue":  {"Förfallen", "Overdue"},
"alarm.title":  {"%d saker når inte fram till Google.", "%d things are not reaching Google."},
```

En nyckel som saknas renderas som `⟦nyckel⟧` i stället för ett tomrum, men den
ska aldrig hinna dit: testerna läser mallarna och koden och underkänner bygget
om någon nyckel saknas, om en översättning är tom, om `%s` inte står i samma
ordning i båda språken, eller om det ligger kvar svensk text i en mall.

---

## Så fungerar det

**Inga konton, inga lösenord.** Man loggar in med Google som ett av
föreningens tre konton. Inloggningen kontrolleras i tre steg: att biljetten
kommer från Google, att den är utfärdad för den här klienten, och att kontot
ligger i rätt Workspace-domän. Sedan slås rollen upp — och den slås upp på
*varje* förfrågan, inte en gång vid inloggningen. Tar man en adress ur
`ACCOUNT_CASHIER` och startar om är den utelåst i samma sekund, inte när kakan
råkar gå ut.

**Loggen överlever medlemmen.** Varje ändring skrivs till en logg som bara
växer, med namnet och adressen som de var vid tillfället. Det är därför en rad
kan tas bort på riktigt: föreningen kan fortfarande svara på "vem tog bort
Anna, och när?" långt efter att Annas rad är borta.

**Sökningen sker i webbläsaren.** Rutan över tabellen filtrerar medan man
skriver, och foldar svenska vokaler — *ostberg* hittar Östberg. Utan
JavaScript är samma ruta ett vanligt formulär som skickas till servern och ger
samma svar, bara en resa senare. Kolumnrubrikerna är länkar, så en filtrerad
och sorterad vy är en adress man kan skicka till resten av styrelsen.

**En knuff, inte en väntan.** Ett tillägg, en ändring eller en avprickning
knuffar igång en synk inom fem sekunder i stället för att vänta på nästa
kvart. Fem medlemmar inlagda i rad blir en körning, inte fem.

---

## Bakom en proxy

Sätt `BASE_URL` till den riktiga adressen. Den styr både vart Google skickar
tillbaka webbläsaren och om sessionskakan sätts som `Secure` (den blir det när
adressen är `https://`). Exempel för Caddy:

```
members.rudbeckia.nu {
    reverse_proxy members:8080
}
```

Nginx behöver skicka vidare `X-Forwarded-For` för att inloggningsspärren ska
räkna rätt IP-adress.

---

## Deploy

Varje push till `main` bygger en image och lägger den i GitHub Packages, för
både `amd64` och `arm64`. Testerna körs först — går de inte igenom publiceras
ingenting.

| Tagg | Sätts när |
|---|---|
| `latest` | vid varje bygge från huvudgrenen, och vid en skarp version (`v1.2.3`) |
| `main` | vid bygge från huvudgrenen |
| `1.2.3`, `1.2` | vid en `v1.2.3`-tagg |
| `sha-abc1234` | vid varje bygge, om du vill låsa en deploy till en exakt commit |

```
ghcr.io/kollektivhuset-rudbeckia/members:latest
```

> **Flyttar du registret till en server för första gången?**
> [docs/deploy.md](docs/deploy.md) går igenom vad som ska kopieras — särskilt
> databasen, som är det enda som inte går att göra om — och i vilken ordning
> överlämningen ska ske så att inte två system slåss om grupperna.

Uppdatera på servern:

```bash
docker compose pull && docker compose up -d
```

Första gången du hämtar från ett privat paket:

```bash
echo $GITHUB_TOKEN | docker login ghcr.io -u <användarnamn> --password-stdin
```

---

## Säkerhetskopiering

Allt ligger i en enda SQLite-fil i volymen `members-data`. Det är
medlemsuppgifter — namn, adresser, telefonnummer — så förvara kopian därefter.

```bash
docker compose exec members /members -version   # kolla att den lever
docker run --rm -v members-data:/data -v "$PWD:/backup" alpine \
    cp /data/members.db /backup/members-$(date +%F).db
```

Databasen körs i WAL-läge, så ta med `-wal`-filen om du kopierar en igång
varande databas — eller stoppa containern först.

Grupperna i Google är i praktiken en andra kopia av vilka medlemmar som finns,
men bara adresserna. Namn, telefonnummer, inträdesdatum och betalningar finns
bara i den här filen.

---

## Utveckling

```bash
make            # visar alla kommandon
make demo       # kör upp en påhittad förening
make check      # formatera, vet:a och testa — kör det före push
```

| Paket | Ansvar |
|---|---|
| `internal/config` | Läser `config.yaml` och miljövariabler, och äger behörighetsmatrisen |
| `internal/store` | SQLite: medlemmar, betalningar, förslag, logg, synkläge |
| `internal/membership` | Föreningens regler: tid som medlem, förfallna avgifter, filter och sortering |
| `internal/auth` | Google-inloggning, signerade sessionskakor |
| `internal/google` | Klient mot Directory, People och Sheets — bara standardbiblioteket |
| `internal/sync` | Jämför registret med Google och rapporterar varje skillnad |
| `internal/importer` | Engångsimporten från kontaktetiketterna |
| `internal/i18n` | Ordkatalogen, datum och språkvalet |
| `internal/web` | Rutter, HTML-mallar, CSS |
| `internal/demo` | Den påhittade föreningen |

Mallar och statiska filer bäddas in i binären med `go:embed`, så det finns
bara en fil att flytta runt. CSS och JavaScript länkas med en hash av
innehållet i adressen (`/static/app.css?v=…`), så en ny version når
webbläsarna direkt i stället för att ligga kvar i deras cache.

Det finns ingen byggkedja för frontend, och Google-klienten är skriven mot
REST-endpointerna med enbart standardbiblioteket. De officiella
klientbiblioteken skulle dra in ett hundratal moduler för att göra det som i
praktiken är ett signerat JWT och ett dussin JSON-anrop — och det som betyder
mest här är inte bekvämlighet utan formuleringen av ett felmeddelande. När en
synk misslyckas ska styrelsen kunna läsa varför på en webbsida.
