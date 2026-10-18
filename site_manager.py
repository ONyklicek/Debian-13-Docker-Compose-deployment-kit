#!/usr/bin/env python3
"""Minimal management app for multiple sites.

Features:
- TUI over SSH (`python3 site_manager.py tui`)
- Local web UI (`python3 site_manager.py web --host 127.0.0.1 --port 8000`)
- Store site definitions in a JSON registry and generate Traefik route config
- Safe, local-only admin UI by default (LAN can be enabled explicitly)
"""

from __future__ import annotations

import argparse
import json
import os
import re
import sys
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from pathlib import Path
from typing import Any, Dict, List

ROOT = Path(__file__).resolve().parent
SITES_FILE = ROOT / "sites.json"
TRAEFIK_FILE = ROOT / "traefik" / "dynamic" / "sites.yml"


def slugify(value: str) -> str:
    return re.sub(r"[^a-z0-9]+", "-", value.lower()).strip("-") or "site"


def default_sites() -> Dict[str, Any]:
    return {
        "sites": [
            {
                "name": "erp",
                "domain": "erp.example.com",
                "upstream": "http://app-blue:8080",
                "enabled": True,
                "kind": "web",
                "notes": "Výchozí ERP web",
            }
        ]
    }


def load_sites() -> List[Dict[str, Any]]:
    if not SITES_FILE.exists():
        SITES_FILE.write_text(json.dumps(default_sites(), ensure_ascii=False, indent=2) + "\n", encoding="utf-8")
    try:
        data = json.loads(SITES_FILE.read_text(encoding="utf-8"))
        sites = data.get("sites", [])
    except json.JSONDecodeError:
        sites = default_sites()["sites"]
    if not isinstance(sites, list):
        sites = default_sites()["sites"]
    cleaned: List[Dict[str, Any]] = []
    for site in sites:
        cleaned.append(
            {
                "name": str(site.get("name", "site")).strip(),
                "domain": str(site.get("domain", "example.com")).strip(),
                "upstream": str(site.get("upstream", "http://app-blue:8080")).strip(),
                "enabled": bool(site.get("enabled", True)),
                "kind": str(site.get("kind", "web")).strip() or "web",
                "notes": str(site.get("notes", "")).strip(),
            }
        )
    return cleaned


def save_sites(sites: List[Dict[str, Any]]) -> None:
    SITES_FILE.write_text(json.dumps({"sites": sites}, ensure_ascii=False, indent=2) + "\n", encoding="utf-8")


def generate_traefik_yaml(sites: List[Dict[str, Any]]) -> str:
    active_sites = [site for site in sites if site.get("enabled")]
    if not active_sites:
        return "http:\n  routers: {}\n  services: {}\n"

    lines: List[str] = ["http:", "  routers:"]
    services: List[str] = ["  services:"]

    for site in active_sites:
        domain = site["domain"]
        slug = slugify(site["name"] or domain)
        service_name = f"{slug}-svc"

        lines.append(f"    {slug}-web:")
        lines.append(f"      rule: \"Host(`{domain}`)\"")
        lines.append("      entryPoints: [websecure]")
        lines.append(f"      service: {service_name}")
        lines.append("      tls:")
        lines.append("        certResolver: le")
        lines.append("      priority: 100")

        lines.append(f"    {slug}-reverb:")
        lines.append(f"      rule: \"Host(`{domain}`) && PathRegexp(`^/app/[^/]+`)\"")
        lines.append("      entryPoints: [websecure]")
        lines.append("      service: reverb-svc")
        lines.append("      tls:")
        lines.append("        certResolver: le")
        lines.append("      priority: 200")

        services.append(f"    {service_name}:")
        services.append("      loadBalancer:")
        services.append("        servers:")
        services.append(f"          - url: \"{site.get('upstream', 'http://app-blue:8080')}\"")

    services.append("    reverb-svc:")
    services.append("      loadBalancer:")
    services.append("        servers:")
    services.append("          - url: \"http://reverb:8080\"")

    lines.extend(services)
    return "\n".join(lines) + "\n"


def write_traefik_config(sites: List[Dict[str, Any]]) -> None:
    TRAEFIK_FILE.parent.mkdir(parents=True, exist_ok=True)
    TRAEFIK_FILE.write_text(generate_traefik_yaml(sites), encoding="utf-8")


class SiteManagerHandler(BaseHTTPRequestHandler):
    server_version = "SiteManager/1.0"

    def do_GET(self) -> None:  # noqa: N802
        if self.path.startswith("/api"):
            self._handle_api()
            return
        self._render_html()

    def do_POST(self) -> None:  # noqa: N802
        length = int(self.headers.get("Content-Length", "0"))
        body = self.rfile.read(length).decode("utf-8")
        form = {}
        for item in body.split("&"):
            if not item:
                continue
            if "=" in item:
                key, value = item.split("=", 1)
                form[key] = value.replace("+", " ")
        sites = load_sites()

        name = form.get("name", "").strip()
        domain = form.get("domain", "").strip()
        upstream = form.get("upstream", "http://app-blue:8080").strip()
        kind = form.get("kind", "web").strip() or "web"
        notes = form.get("notes", "").strip()
        enabled = form.get("enabled", "on") == "on"

        if name and domain:
            sites.append({
                "name": name,
                "domain": domain,
                "upstream": upstream or "http://app-blue:8080",
                "enabled": enabled,
                "kind": kind,
                "notes": notes,
            })
            save_sites(sites)
            write_traefik_config(sites)
        self.send_response(302)
        self.send_header("Location", "/")
        self.end_headers()

    def _handle_api(self) -> None:
        self.send_response(200)
        self.send_header("Content-Type", "application/json; charset=utf-8")
        self.end_headers()
        self.wfile.write(json.dumps({"sites": load_sites()}, ensure_ascii=False).encode("utf-8"))

    def _render_html(self) -> None:
        sites = load_sites()
        rows = "".join(
            (
                "<tr>"
                f"<td>{h(site.get('name',''))}</td>"
                f"<td>{h(site.get('domain',''))}</td>"
                f"<td>{h(site.get('upstream',''))}</td>"
                f"<td>{h(site.get('kind','web'))}</td>"
                f"<td>{'Ano' if site.get('enabled', True) else 'Ne'}</td>"
                f"<td>{h(site.get('notes',''))}</td>"
                "</tr>"
            )
            for site in sites
        )
        html = f"""
        <html><head><meta charset="utf-8"><title>Site Manager</title>
        <style>
            body {{ font-family: system-ui, sans-serif; margin: 2rem; background: #0b1020; color: #eaf2ff; }}
            table {{ border-collapse: collapse; width: 100%; margin-top: 1rem; }}
            th, td {{ border: 1px solid #2e3d63; padding: 0.7rem; text-align: left; }}
            th {{ background: #1a2340; }}
            form {{ margin-top: 1.5rem; display: grid; grid-template-columns: repeat(2, minmax(180px, 1fr)); gap: 1rem; }}
            input, select {{ width: 100%; padding: 0.6rem; border-radius: 6px; border: 1px solid #3b4b78; background: #111827; color: white; }}
            button {{ padding: 0.7rem 1rem; border: none; border-radius: 8px; background: #3b82f6; color: white; cursor: pointer; }}
            .full {{ grid-column: 1 / -1; }}
        </style></head>
        <body>
            <h1>Site Manager</h1>
            <p>Správa webů v privátní LAN. Výchozí režim je lokální, ne veřejný.</p>
            <table>
                <thead><tr><th>Name</th><th>Domain</th><th>Upstream</th><th>Kind</th><th>Enabled</th><th>Notes</th></tr></thead>
                <tbody>{rows}</tbody>
            </table>
            <form method="post">
                <div><label>Name<input name="name" required></label></div>
                <div><label>Domain<input name="domain" required placeholder="app.example.com"></label></div>
                <div class="full"><label>Upstream<input name="upstream" value="http://app-blue:8080"></label></div>
                <div><label>Kind<select name="kind"><option value="web">web</option><option value="api">api</option></select></label></div>
                <div><label>Enabled<input type="checkbox" name="enabled" checked></label></div>
                <div class="full"><label>Notes<input name="notes" placeholder="Volitelný popis"></label></div>
                <div class="full"><button type="submit">Přidat web</button></div>
            </form>
        </body></html>
        """
        self.send_response(200)
        self.send_header("Content-Type", "text/html; charset=utf-8")
        self.end_headers()
        self.wfile.write(html.encode("utf-8"))


def h(value: Any) -> str:
    return str(value).replace("&", "&amp;").replace("<", "&lt;").replace(">", "&gt;")


def run_web(host: str, port: int) -> None:
    httpd = ThreadingHTTPServer((host, port), SiteManagerHandler)
    print(f"Web UI běží na http://{host}:{port}")
    httpd.serve_forever()


def run_tui() -> None:
    print("Site Manager TUI")
    print("================")
    while True:
        print("\n1) Seznam webů")
        print("2) Přidat web")
        print("3) Vygenerovat Traefik config")
        print("4) Uložit a ukončit")
        choice = input("Vyber: ").strip()
        sites = load_sites()
        if choice == "1":
            for idx, site in enumerate(sites, 1):
                status = "ON" if site.get("enabled", True) else "OFF"
                print(f"{idx}. {site['name']} | {site['domain']} | {site['upstream']} | {status}")
        elif choice == "2":
            name = input("Jméno webu: ").strip()
            domain = input("Doména: ").strip()
            upstream = input("Upstream (výchozí http://app-blue:8080): ").strip() or "http://app-blue:8080"
            kind = input("Typ (web/api): ").strip() or "web"
            enabled = input("Povolit (y/N): ").strip().lower() in {"y", "yes", "1"}
            notes = input("Poznámka: ").strip()
            if name and domain:
                sites.append({
                    "name": name,
                    "domain": domain,
                    "upstream": upstream,
                    "enabled": enabled,
                    "kind": kind,
                    "notes": notes,
                })
                save_sites(sites)
                write_traefik_config(sites)
                print("Web přidán.")
            else:
                print("Jméno a doména jsou povinné.")
        elif choice == "3":
            write_traefik_config(sites)
            print("Traefik config zapsána do", TRAEFIK_FILE)
        elif choice == "4":
            break
        else:
            print("Neplatná volba.")


def main() -> int:
    parser = argparse.ArgumentParser(description="Správa webů pro Docker/Traefik stack")
    parser.add_argument("mode", nargs="?", choices=["tui", "web", "list", "generate"], default="list")
    parser.add_argument("--host", default="127.0.0.1")
    parser.add_argument("--port", type=int, default=8000)
    args = parser.parse_args()

    if args.mode == "list":
        for site in load_sites():
            print(f"{site['name']} | {site['domain']} | {site['upstream']} | {'ON' if site.get('enabled', True) else 'OFF'}")
        return 0
    if args.mode == "generate":
        sites = load_sites()
        write_traefik_config(sites)
        print(f"Traefik config generated: {TRAEFIK_FILE}")
        return 0
    if args.mode == "tui":
        run_tui()
        return 0
    if args.mode == "web":
        run_web(args.host, args.port)
        return 0
    return 1


if __name__ == "__main__":
    raise SystemExit(main())
