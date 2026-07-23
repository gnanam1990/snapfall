import { readFile } from 'node:fs/promises';
import { resolve } from 'node:path';
import { validateCircleV1Fixture } from './at18-fixture.js';

const fixtureArgument = process.argv[2];
if (!fixtureArgument) {
  throw new Error('usage: npm run at18:fixture -- <path-to-v1-fixture.json>');
}

const fixturePath = resolve(fixtureArgument);
const fixture = JSON.parse(await readFile(fixturePath, 'utf8')) as unknown;
validateCircleV1Fixture(fixture);

console.log(`AT-18 Circle facilitator endpoints verified in ${fixturePath}`);
