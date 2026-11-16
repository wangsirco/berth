import os
import pwd

from django.contrib.auth.models import User
from django.http import HttpResponse
from django.urls import path


def tryw(p):
    try:
        with open(p, "w") as f:
            f.write("x")
        return "ok"
    except OSError:
        return "denied"


urlpatterns = [
    path("", lambda r: HttpResponse(open("VERSION").read().strip())),
    path("whoami", lambda r: HttpResponse(pwd.getpwuid(os.getuid()).pw_name)),
    path("w-code", lambda r: HttpResponse(tryw("manage.py"))),
    path("w-data", lambda r: HttpResponse(tryw("data/probe.txt"))),
    path("users", lambda r: HttpResponse(str(User.objects.count()))),
]
