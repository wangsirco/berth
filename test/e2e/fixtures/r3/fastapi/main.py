import os
import pwd

from fastapi import FastAPI
from fastapi.responses import PlainTextResponse

app = FastAPI()


def tryw(p):
    try:
        with open(p, "w") as f:
            f.write("x")
        return "ok"
    except OSError:
        return "denied"


@app.get("/", response_class=PlainTextResponse)
def index():
    return open("VERSION").read().strip()


@app.get("/whoami", response_class=PlainTextResponse)
def whoami():
    return pwd.getpwuid(os.getuid()).pw_name


@app.get("/w-code", response_class=PlainTextResponse)
def wcode():
    return tryw("main.py")


@app.get("/w-data", response_class=PlainTextResponse)
def wdata():
    return tryw("data/probe.txt")
