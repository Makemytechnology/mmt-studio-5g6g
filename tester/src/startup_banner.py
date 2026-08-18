# Copyright (c) 2026 MakeMyTechnology. All rights reserved.
# src/startup_banner.py — log a one-shot startup banner with platform info.
#
# Called once at boot from app.py. Prints a block of platform + version
# metadata so that bug reports always carry enough context to reproduce.
#
#     from src.startup_banner import log_banner
#     log_banner()

import os
import platform
import sys

from src.tester_logger import get_logger

log = get_logger('startup')

# Python packages to report in the banner.
_REPORTED_PACKAGES = (
    "fastapi",
    "uvicorn",
    "starlette",
    "pydantic",
    "pysctp",
    "cryptography",
    "jinja2",
    "websockets",
    "pycrate",
)


def _project_version():
    """Read the project version from src/build/version.py."""
    try:
        from src.build.version import VERSION
        return VERSION
    except Exception:
        return "unknown"


def _package_version(name):
    """Return the installed version of a package, or None."""
    try:
        from importlib.metadata import PackageNotFoundError, version
        try:
            return version(name)
        except PackageNotFoundError:
            pass
    except ImportError:
        pass

    try:
        mod = __import__(name)
        v = getattr(mod, "__version__", None)
        if v:
            return str(v)
    except Exception:
        pass

    return None


def _libc_version():
    """Return libc version, e.g. 'glibc 2.39'."""
    name, ver = platform.libc_ver()
    if name and ver:
        return f"{name} {ver}"
    return "unknown"


def _os_pretty_name():
    """Return the distribution PRETTY_NAME from /etc/os-release."""
    try:
        with open("/etc/os-release", encoding="utf-8") as f:
            for line in f:
                if line.startswith("PRETTY_NAME="):
                    return line.partition("=")[2].strip().strip('"')
    except OSError:
        pass
    return "unknown"


def _virtualization():
    """Detect virtualization / container environment."""
    import subprocess

    try:
        out = subprocess.run(
            ["systemd-detect-virt"],
            capture_output=True, text=True, timeout=2,
        )
        v = out.stdout.strip()
        if v and v != "none":
            return {"oracle": "virtualbox", "microsoft": "hyper-v"}.get(v, v)
        if v == "none":
            return "bare metal"
    except (OSError, subprocess.SubprocessError):
        pass

    try:
        with open("/sys/class/dmi/id/sys_vendor", encoding="utf-8") as f:
            vendor = f.read().strip().lower()
        if "virtualbox" in vendor or "innotek" in vendor:
            return "virtualbox"
        if "vmware" in vendor:
            return "vmware"
        if "qemu" in vendor or "kvm" in vendor:
            return "kvm/qemu"
        if "microsoft" in vendor:
            return "hyper-v"
        if "xen" in vendor:
            return "xen"
    except OSError:
        pass

    try:
        with open("/proc/cpuinfo", encoding="utf-8") as f:
            if "hypervisor" in f.read():
                return "virtualized (unknown hypervisor)"
    except OSError:
        pass

    return "bare metal"


def _cpu_model():
    """Return a single-line CPU description."""
    try:
        with open("/proc/cpuinfo", encoding="utf-8") as f:
            text = f.read()
    except OSError:
        return "unknown"

    for line in text.splitlines():
        if line.startswith("model name"):
            return line.partition(":")[2].strip()

    model = ""
    hardware = ""
    for line in text.splitlines():
        if line.startswith("Model"):
            model = line.partition(":")[2].strip()
        elif line.startswith("Hardware"):
            hardware = line.partition(":")[2].strip()
    return model or hardware or "unknown"


def _ram_total_gb():
    """Return total RAM in gigabytes (rounded to one decimal)."""
    try:
        with open("/proc/meminfo", encoding="utf-8") as f:
            for line in f:
                if line.startswith("MemTotal:"):
                    kb = int(line.split()[1])
                    return round(kb / (1024 * 1024), 1)
    except (OSError, ValueError, IndexError):
        pass
    return None


def log_banner():
    """Log the startup banner."""
    project_ver = _project_version()
    py_ver = sys.version.split()[0]
    py_impl = platform.python_implementation()
    sysname = platform.system()
    release = platform.release()
    machine = platform.machine()
    libc = _libc_version()
    os_name = _os_pretty_name()
    virt = _virtualization()
    cpu = _cpu_model()
    cores = os.cpu_count() or "?"
    ram_gb = _ram_total_gb()
    host = platform.node() or "unknown"

    bar = "=" * 72
    log.info(bar)
    log.info("SA Tester %s", project_ver)
    log.info("Host:    %s  (%s)", host, virt)
    log.info("OS:      %s", os_name)
    log.info("Kernel:  %s %s (%s)", sysname, release, machine)
    log.info("libc:    %s", libc)
    log.info("CPU:     %s", cpu)
    if ram_gb is not None:
        log.info("Cores:   %s   RAM: %s GB", cores, ram_gb)
    else:
        log.info("Cores:   %s", cores)
    log.info("Python:  %s %s", py_impl, py_ver)
    log.info("Python packages:")
    for pkg in _REPORTED_PACKAGES:
        ver = _package_version(pkg)
        log.info("  %-14s %s", pkg, ver if ver else "(not installed)")
    log.info(bar)
