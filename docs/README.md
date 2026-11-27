# Documentation

- [Français](./fr)
- [English](./en)
- [Español](./es)

Each language holds two settings files read by
[xolo-gateway/docs](https://github.com/xolo-gateway/docs) when it builds the
site:

- `nav.toml`, the menu. A page left out of it is published but not listed, so
  add every new page there.
- `redirects.txt`, one `old/path/ new/path/` line per moved page, so that
  links to the former address keep working.
