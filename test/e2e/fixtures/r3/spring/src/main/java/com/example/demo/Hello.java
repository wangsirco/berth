package com.example.demo;

import java.nio.file.Files;
import java.nio.file.Path;
import org.springframework.web.bind.annotation.GetMapping;
import org.springframework.web.bind.annotation.RestController;

@RestController
public class Hello {
	String tryw(String p) {
		try {
			Files.writeString(Path.of(p), "x");
			return "ok";
		} catch (Exception e) {
			return "denied";
		}
	}

	@GetMapping("/")
	public String index() throws Exception { return Files.readString(Path.of("VERSION")).trim(); }

	@GetMapping("/whoami")
	public String who() { return System.getProperty("user.name"); }

	@GetMapping("/w-code")
	public String wcode() { return tryw("pom.xml"); }

	@GetMapping("/w-data")
	public String wdata() { return tryw("data/probe.txt"); }
}
