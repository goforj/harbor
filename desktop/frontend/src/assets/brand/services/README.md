# Service identity artwork

These bundled SVGs provide offline service identities. Keep logos separate from runtime status indicators.

Except for MySQL and Mailpit, the monochrome vendor marks come from Simple Icons 16.34.0, distributed by `@iconify-json/simple-icons` 1.2.99 under CC0-1.0. The path data is copied without alteration and filled with the listed presentation colors. Source: https://github.com/simple-icons/simple-icons/tree/16.34.0/icons. No package dependency is added to Harbor.

Mailpit uses the upstream project logo from https://github.com/axllent/mailpit/blob/4045859fe6422a4bbdbc1b87e643903b609b3667/server/ui/mailpit.svg. The SVG is copied unchanged. SHA-256: `f5863cd6ba165411a3f4b770990947321d9e5a9d992e39e2c9b98fb92af1ab90`.

Add artwork through the explicit service catalog in `src/lib/serviceArtwork.ts`. An image reference takes precedence over name aliases. Match complete repositories, including registry and namespace, instead of assigning vendor logos to unrelated images with similar names. Current Harbor snapshots provide names and IDs; image-aware matching is ready for a future image metadata projection. Unknown services retain their semantic icon. Never fetch logos or execute SVG markup at runtime.

MySQL uses Devicon's compact dolphin artwork, copied unchanged from https://github.com/devicons/devicon/blob/6a37858047b0e8d9c9c0e68f4a071fc410652e89/icons/mysql/mysql-original.svg. Its MIT license is retained in `LICENSE.devicon`. This avoids rendering a small wordmark as an icon.
