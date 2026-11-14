# Home videos and photos

**Photos and videos** shows the pictures and home videos you already keep on
your server: everything from every folder you give it, newest first, by month
and day, with albums Box Office makes for days out and trips, albums of your
own, and the copies you've kept more than once.

Box Office reads your files and never changes, moves or renames them. The one
exception is **Copies**, where you choose copies to move to a recycle folder
yourself (see [Copies](#copies)).

## Turning it on

1. In **Settings → Library**, turn on **Home videos and photos** under **More
   than movies and TV**. It's off until you do, and nothing about it shows until
   then.
2. Under **Home videos and photos root folders**, add the folder your photos and
   videos are in. Box Office suggests `/media/photos`, `/media/pictures`,
   `/media/home-videos` and the like when it can see one. Add more than one if
   they're in different places, such as a phone backup folder and an old camera
   folder.
3. Box Office starts looking through the folder straight away. **Photos and
   videos** in the sidebar shows how far it has got. After that it looks again
   twice a day, and **Scan** on the page looks now.

Any layout works: by year, by event, a phone's `DCIM` copied as it was, or
everything in one folder. Hidden folders (starting with `.`), recycle bins,
Synology's `@eaDir` and the like are left out.

## What it reads

| | |
|---|---|
| Photos | JPEG, PNG, GIF, WebP, AVIF, BMP, HEIC/HEIF (iPhone), TIFF, and raw files: DNG, CR2, CR3, NEF, ARW, RAF, ORF, RW2, PEF, SRW |
| Videos | MP4, MOV, M4V, MKV, AVI, MTS/M2TS, 3GP, MPG, WMV, WebM, FLV, DV |

For each one it reads:

- **When it was taken**, from the photo's EXIF or the video's own creation
  date (an iPhone's `com.apple.quicktime.creationdate` first, which keeps the
  local time). Without either, it reads the date in the file's name, as phones
  and apps write them (`IMG_20240712_103709.jpg`, `PXL_20240712_103709160.jpg`,
  `2024-07-12 10.37.09.jpg`, `VID-20240712-WA0003.mp4`), and only then the
  file's modified time. The info panel says which it used.
- **Where**, from the GPS position a phone saves in it, named as the nearest
  town (see [Places](#places)).
- **The camera** and the picture's size, and a video's length.

Thumbnails are made for the grid (480 pixels) and kept in Box Office's data
folder under `home/`. Photos a browser can't show (HEIC, TIFF and raw) also get
a viewable copy up to 2048 pixels; raw photos use the preview the camera saved
inside them. Videos a browser can't play are listed and can be downloaded, but
not played in the page. HEIC, WebP, TIFF and videos need FFmpeg, which is in the
image; running from source, put `ffmpeg` and `ffprobe` on the `PATH`.

A photo turned on its side by the camera (EXIF orientation) is shown the right
way up.

## The timeline

Everything, newest first, under month and day headings. **Photos** and
**Videos** show just one kind. Click one to see it full size: the arrow keys or
a swipe go to the next, **Info** shows when and where it was taken, the camera
and the file, and **Download the original** gets the file as it is. **Select**
picks several to add to an album.

## Albums

**Made by Box Office** albums are found from when and where things were taken:
photos and videos taken together, with no gap of more than 8 hours, make one,
named after the place most of them were taken. A trip away from home stays one
album across the nights, as long as the photos on each side of the gap were
taken away from home and near each other. An album needs at least 8 photos and
videos, so a few snaps on an ordinary day don't make one. They're found again
after each scan; a name you give one is kept, and **Put away** hides one you
don't want. Put away albums are listed under **Put away** at the bottom of the
page, and **Bring back** on one lists it with the others again.

**Your albums** are your own: **New album**, then add to it from the timeline
(**Select**) or from a photo's info. Albums are kept in Box Office's database,
not as folders, so an album never copies or moves a file, and a photo can be in
as many as you like. Deleting one of your albums deletes the album, not its
photos.

## Copies

**Copies** lists photos and videos you've kept more than once:

- **Same file**: the same bytes in two places, such as a phone backup copied in
  twice.
- **Looks the same**: a smaller or recompressed copy of a photo, such as one
  sent through WhatsApp or exported for the web, taken at the same moment.
  Photos are compared by what they look like, and only those taken within 10
  minutes of each other, so a burst of different shots isn't taken for copies.

Box Office keeps the biggest copy of each (the most pixels, then the biggest
file) and ticks the others. Untick any you want to keep, then **Move to recycle
folder**: the ticked copies go to the `.recycle` folder in their root folder,
under the day's date, so you can get them back until you empty it (or until
**Settings → Library → Recycle folders** deletes it). Box Office never moves the
last copy of anything, and checks the copies are still the size they were before
moving them. Moving needs the root folder to be writable; everything else works
on a read-only one.

## Places

Positions are named with the [GeoNames](https://www.geonames.org/) list of
towns of more than 15,000 people (CC BY 4.0), downloaded once into the data
folder the first time it's needed. A photo is named after the nearest town
within 60 km, with its country. Nothing about your photos is sent anywhere.

`BOXOFFICE_PLACES_URL` points it at another copy of `cities15000.zip` (or the
`.txt` inside it, as a URL or a file path), or `off` to not name places; photos
still keep their positions, and albums are still found by time and distance.

## API

| | |
|---|---|
| `GET /api/v1/home` | How many photos and videos, and the last scan |
| `POST /api/v1/home/scan` | Look through the root folders now |
| `GET /api/v1/home/items` | The timeline, newest first: `kind=photo\|video`, `album=`, `limit=` (up to 500) and `before=` the last page's `next` |
| `GET /api/v1/home/months` | How many in each month |
| `GET /api/v1/home/items/{id}` | One, with the albums it's in |
| `GET /api/v1/home/items/{id}/thumb`, `/view`, `/file` | Its thumbnail, a copy a browser can show, and the original |
| `GET`/`POST /api/v1/home/albums` | Albums (`hidden=true` to include the put away ones); make one with `name` and `items` |
| `GET`/`PATCH`/`DELETE /api/v1/home/albums/{id}` | One album; `PATCH` takes `name`, `hidden` and `coverId`; deleting one of Box Office's puts it away |
| `POST /api/v1/home/albums/{id}/items`, `/remove` | Add or take out `items` |
| `GET /api/v1/home/duplicates` | Copies |
| `POST /api/v1/home/duplicates/recycle` | Move `items` to the recycle folder; `size` is their total as shown |
