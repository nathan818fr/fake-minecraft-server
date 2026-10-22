#!/usr/bin/env -S uv run --script
# /// script
# requires-python = ">=3.12"
# dependencies = ["pillow==12.0.0"]
# ///
"""Takes the screenshots of the documentation in Minecraft.

Runs the examples of examples/, starts the official client in a headless
Wayland compositor (weston), as an X client of its Xwayland, opens its server
list, and crops what it shows.

Needs weston, Xwayland, xdotool, and the Java version that the client
requires. The client, its libraries and its assets (but sounds) are downloaded
from Mojang, checked against their SHA-1, and cached. The client runs with an
offline profile, as mod development tools run it.
"""

import argparse
import hashlib
import json
import os
import re
import shutil
import signal
import struct
import subprocess
import sys
import tempfile
import time
import urllib.request
from pathlib import Path

from PIL import Image

# The version of Minecraft started, whose layout the positions below follow.
CLIENT_VERSION = "26.3"
MANIFEST_URL = "https://piston-meta.mojang.com/mc/game/version_manifest_v2.json"

# The window, at GUI scale 2, which the positions below, in pixels, assume.
WIDTH, HEIGHT = 1708, 960
MULTIPLAYER_BUTTON = (854, 404)
# The first entry of the server list, and its player counts.
FIRST_ENTRY = (760, 100)
FIRST_ENTRY_COUNTS = (1030, 84)
# Crops, as (left, top, right, bottom).
SERVER_LIST_CROP = (530, 8, 530 + 648, 8 + 280)  # The title and three entries.
HOVER_CROP = (538, 44, 538 + 912, 44 + 104)  # The first entry and its tooltip.
KICK_CROP = (534, 374, 534 + 640, 374 + 222)  # The message after a kick.

# The servers of the list: (name, example, address).
OPENING = ("Example Network", "examples/opening-soon.yaml", "127.0.0.1:25711")
HYDONUT = ("HyDonut", "examples/hydonut.yaml", "127.0.0.1:25712")
FUNCRAFT = ("FunCraft", "examples/funcraft.yaml", "127.0.0.1:25713")

# No first-launch screens, nor sound, and the panorama behind the menus still,
# so that the screenshots are the same at each run.
OPTIONS = """onboardAccessibility:false
panoramaScrollSpeed:0.0
skipMultiplayerWarning:true
joinedFirstServer:true
tutorialStep:none
guiScale:2
fullscreen:false
lang:en_us
narrator:0
soundCategory_master:0.0
"""

# Logged once the client has loaded its GUI textures, a few seconds before it
# shows the title screen.
READY_LINE = re.compile(r"Created: .* minecraft:textures/atlas/gui\.png-atlas")


def main():
    parser = argparse.ArgumentParser(description=__doc__.splitlines()[0])
    parser.add_argument("--out", type=Path, default=Path("docs/screenshots"))
    out = parser.parse_args().out
    for tool in ("weston", "Xwayland", "xdotool", "go", "java"):
        if not shutil.which(tool):
            sys.exit(f"{tool} is required")
    out.mkdir(parents=True, exist_ok=True)
    cache = Path(os.environ.get("XDG_CACHE_HOME", Path.home() / ".cache"), "fake-minecraft-server", "screenshots")

    print("preparing the client")
    version, classpath = prepare_client(cache)
    procs = []
    with tempfile.TemporaryDirectory(prefix="fms-screenshots") as tmp:
        try:
            start_examples(procs, Path(tmp), [OPENING, HYDONUT, FUNCRAFT])
            display = start_compositor(procs, cache)
            print("starting the client")
            game = start_client(procs, cache, version, classpath, display)
            x = Display(display, game / "screenshots")
            shots = [
                ("server-list.png", SERVER_LIST_CROP, [OPENING, HYDONUT, FUNCRAFT], None),
                # HyDonut alone, so that the tooltip of its player counts, which
                # rises above the mouse, covers no other entry.
                ("server-list-hover.png", HOVER_CROP, [HYDONUT], lambda: x.hover(FIRST_ENTRY_COUNTS)),
                # The kick message of Example Network, after joining it.
                ("kick.png", KICK_CROP, [OPENING], lambda: x.join(FIRST_ENTRY)),
            ]
            for i, (name, crop, servers, act) in enumerate(shots):
                if i > 0:
                    x.close_server_list()
                # The client reads its server list each time it opens it.
                (game / "servers.dat").write_bytes(servers_dat(servers))
                x.open_server_list()
                if act:
                    act()
                x.save(out / name, crop)
        finally:
            stop(procs)
    print(f"screenshots in {out}")


def prepare_client(cache):
    """Downloads the client, its libraries and its assets but sounds."""
    manifest = json.loads(get(MANIFEST_URL))
    url = next((v["url"] for v in manifest["versions"] if v["id"] == CLIENT_VERSION), None)
    if not url:
        sys.exit(f"version {CLIENT_VERSION} not found")
    version = json.loads(get(url))
    check_java(version["javaVersion"]["majorVersion"])

    classpath = [
        fetch(art["url"], cache / "libraries" / art["path"], art["sha1"])
        for lib in version["libraries"]
        if (art := lib["downloads"].get("artifact")) and allowed(lib.get("rules"))
    ]
    client = version["downloads"]["client"]
    classpath.append(fetch(client["url"], cache / f"client-{CLIENT_VERSION}.jar", client["sha1"]))

    index = version["assetIndex"]
    index_path = fetch(index["url"], cache / "assets" / "indexes" / f"{index['id']}.json", index["sha1"])
    objects = json.loads(index_path.read_text())["objects"]
    for name, obj in objects.items():
        if not name.startswith(("minecraft/sounds/", "minecraft/music/")):
            h = obj["hash"]
            fetch(f"https://resources.download.minecraft.net/{h[:2]}/{h}", cache / "assets" / "objects" / h[:2] / h, h)
    return version, classpath


def allowed(rules, features=None):
    """Reports whether rules allow something on Linux x86-64 with features."""
    ok = not rules
    for rule in rules or []:
        system = rule.get("os", {})
        match = system.get("name", "linux") == "linux" and system.get("arch", "x86_64") == "x86_64"
        match = match and all((features or {}).get(f, False) == v for f, v in rule.get("features", {}).items())
        if match:
            ok = rule["action"] == "allow"
    return ok


def check_java(want):
    out = subprocess.run(["java", "-version"], capture_output=True, text=True).stderr
    got = re.search(r'version "(\d+)', out)
    if not got or int(got[1]) < want:
        sys.exit(f"java {want} or later is required, found: {out}")


def get(url):
    """Downloads url, retrying a few times, as one of a few hundred downloads
    can fail for no reason."""
    for attempt in range(4):
        try:
            with urllib.request.urlopen(url, timeout=120) as resp:
                return resp.read()
        except OSError:
            if attempt == 3:
                raise
            time.sleep(2)


def fetch(url, path, sha1):
    """Downloads url to path, unless already there, and checks its SHA-1."""
    if not path.exists():
        data = get(url)
        if hashlib.sha1(data).hexdigest() != sha1:
            sys.exit(f"{url}: SHA-1 mismatch")
        path.parent.mkdir(parents=True, exist_ok=True)
        tmp = path.with_name(path.name + ".tmp")
        tmp.write_bytes(data)
        tmp.replace(path)
    return path


def servers_dat(servers):
    """Returns the server list of servers, an uncompressed NBT compound
    {servers: [{name, ip}, ...]}."""

    def string(s):
        return struct.pack(">H", len(s.encode())) + s.encode()

    entries = b"".join(
        b"\x08" + string("name") + string(name) + b"\x08" + string("ip") + string(addr) + b"\x00"
        for name, _, addr in servers
    )
    header = b"\x0a" + string("") + b"\x09" + string("servers") + b"\x0a" + struct.pack(">i", len(servers))
    return header + entries + b"\x00"


def start(procs, args, log=None, **kwargs):
    """Starts args, its output written to log, or discarded."""
    with open(log or os.devnull, "w") as output:
        procs.append(subprocess.Popen(args, stdout=output, stderr=subprocess.STDOUT, **kwargs))


def stop(procs):
    for proc in reversed(procs):
        proc.send_signal(signal.SIGINT)
        try:
            proc.wait(10)
        except subprocess.TimeoutExpired:
            proc.kill()
            proc.wait()


def start_examples(procs, tmp, servers):
    """Builds the server in tmp and runs the examples of servers."""
    binary = tmp / "fake-minecraft-server"
    subprocess.run(["go", "build", "-o", binary, "./cmd/fake-minecraft-server"], check=True)
    for _, example, addr in servers:
        env = os.environ | {"FMS_CONFIG_FILE": example, "FMS_LISTEN": addr}
        start(procs, [binary], tmp / (Path(example).name + ".log"), env=env)
    time.sleep(3)  # Upstreams answer their first poll in well under this.


def start_compositor(procs, cache):
    """Starts weston, headless, with its config and log in cache, and returns
    the X display of its Xwayland. The pixman renderer keeps Xwayland off the
    GPU, so that the client renders in software, the same on every machine;
    the kiosk shell shows the client full screen, at the positions above."""
    cache.mkdir(parents=True, exist_ok=True)
    config, log = cache / "weston.ini", cache / "weston.log"
    config.write_text(f"[xwayland]\npath={shutil.which('Xwayland')}\n")
    log.unlink(missing_ok=True)  # weston appends to it, and the display is read from it.
    start(
        procs,
        [
            "weston",
            "--backend=headless",
            "--fake-seat",
            "--renderer=pixman",
            "--shell=kiosk",
            f"--width={WIDTH}",
            f"--height={HEIGHT}",
            "--xwayland",
            f"--config={config}",
            f"--socket=fms-screenshots-{os.getpid()}",
            f"--log={log}",
        ],
    )
    for _ in range(50):
        time.sleep(0.2)
        if log.exists() and (m := re.search(r"xserver listening on display (:\d+)", log.read_text())):
            return m[1]
    sys.exit(f"weston did not start Xwayland, see {log}")


def start_client(procs, cache, version, classpath, display):
    """Starts the client on display, waits for its title screen, and returns
    its game directory."""
    game, log = cache / "game", cache / "game.log"
    game.mkdir(parents=True, exist_ok=True)
    (game / "options.txt").write_text(OPTIONS)
    values = {
        "auth_player_name": "Steve",
        "version_name": CLIENT_VERSION,
        "game_directory": str(game),
        "assets_root": str(cache / "assets"),
        "assets_index_name": version["assetIndex"]["id"],
        "auth_uuid": "00000000000040008000000000000001",
        "auth_access_token": "0",
        "clientid": "0",
        "auth_xuid": "0",
        "user_type": "legacy",
        "version_type": "release",
        "natives_directory": str(cache / "natives"),
        "launcher_name": "fake-minecraft-server-screenshots",
        "launcher_version": "1",
        "classpath": os.pathsep.join(map(str, classpath)),
        "resolution_width": str(WIDTH),
        "resolution_height": str(HEIGHT),
    }
    features = {"has_custom_resolution": True}
    args = version["arguments"]
    jvm, game_args = expand(args["jvm"], values, features), expand(args["game"], values, features)
    command = ["java", "-Xmx2G", *jvm, version["mainClass"], *game_args]
    env = os.environ | {"DISPLAY": display, "WAYLAND_DISPLAY": "", "SDL_VIDEO_DRIVER": "x11"}
    start(procs, command, log, env=env, cwd=game)  # It writes its crash reports in its directory.

    deadline = time.monotonic() + 180
    while time.monotonic() < deadline:
        time.sleep(1)
        if log.exists() and READY_LINE.search(log.read_text(errors="replace")):
            time.sleep(5)  # The title screen fades in.
            return game
    sys.exit(f"the client did not start, see {log}")


def expand(args, values, features):
    """Returns the arguments of the version JSON, as strings or as
    {rules, value}, with their ${placeholders} replaced."""
    out = []
    for arg in args:
        if isinstance(arg, str):
            out.append(re.sub(r"\$\{([a-z_]+)\}", lambda m: values.get(m[1], ""), arg))
        elif allowed(arg["rules"], features):
            value = arg["value"]
            out += expand(value if isinstance(value, list) else [value], values, features)
    return out


class Display:
    """Drives the client on an X display, with xdotool."""

    def __init__(self, name, screenshots):
        self.env = os.environ | {"DISPLAY": name}
        self.screenshots = screenshots  # Where the client writes the screenshots of its F2 key.

    def xdotool(self, *args):
        subprocess.run(["xdotool", *map(str, args)], env=self.env, check=True)

    def click(self, point, count=1):
        self.xdotool("mousemove", *point)
        time.sleep(0.3)
        self.xdotool("click", "--repeat", count, "--delay", 120, 1)

    def away(self):
        """Moves the mouse to a corner, where it hovers nothing."""
        self.xdotool("mousemove", 1, 1)

    def open_server_list(self):
        """Opens the server list from the title screen, and waits for the
        client to ping the servers."""
        self.click(MULTIPLAYER_BUTTON)
        time.sleep(4)
        self.away()

    def close_server_list(self):
        self.xdotool("key", "Escape")
        time.sleep(2)

    def hover(self, point):
        self.xdotool("mousemove", *point)
        time.sleep(1.5)

    def join(self, point):
        """Joins the server of the entry at point, and waits for the screen that
        tells why the connection ended."""
        self.click(point, 2)
        time.sleep(4)
        self.away()

    def save(self, path, crop):
        """Takes a screenshot with the F2 key of the client, which writes the
        whole window, and saves its part crop to path."""
        shutil.rmtree(self.screenshots, ignore_errors=True)  # The new one is then alone.
        self.xdotool("key", "F2")
        for _ in range(50):
            time.sleep(0.2)
            files = list(self.screenshots.glob("*.png"))
            if len(files) == 1:
                try:
                    with Image.open(files[0]) as img:
                        img.crop(crop).save(path, optimize=True)
                    return
                except OSError:  # The client writes it in the background.
                    pass
        sys.exit(f"the client wrote no screenshot in {self.screenshots}")


if __name__ == "__main__":
    main()
