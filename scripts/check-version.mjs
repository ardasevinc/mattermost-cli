import { readFile } from "node:fs/promises";

const packageJson = JSON.parse(await readFile(new URL("../package.json", import.meta.url), "utf8"));
const skill = await readFile(new URL("../skills/mattermost-cli/SKILL.md", import.meta.url), "utf8");
const skillVersion = skill.match(/^version:\s*([^\s]+)\s*$/m)?.[1];

if (!skillVersion) {
  throw new Error("Could not find a version in skills/mattermost-cli/SKILL.md");
}

if (packageJson.version !== skillVersion) {
  throw new Error(
    `Version mismatch: package.json is ${packageJson.version}, skill is ${skillVersion}`,
  );
}

const releaseTag = process.env.RELEASE_TAG;
if (releaseTag) {
  const expectedTag = `v${packageJson.version}`;
  if (releaseTag !== expectedTag) {
    throw new Error(
      `Version mismatch: expected release tag ${expectedTag}, got ${releaseTag}`,
    );
  }
}

console.log(`Version check passed: ${packageJson.version}`);
