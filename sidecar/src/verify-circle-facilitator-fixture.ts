import { readFile } from 'node:fs/promises';
import { resolve } from 'node:path';
import { validateCircleV1Fixture } from './circle-facilitator-fixture.js';

const fixtureArgument = process.argv[2];
if (!fixtureArgument) {
  throw new Error(
    'usage: npm run verify:circle-facilitator-fixture -- <path-to-v1-fixture.json>',
  );
}

const fixturePath = resolve(fixtureArgument);
const fixture = JSON.parse(await readFile(fixturePath, 'utf8')) as unknown;
validateCircleV1Fixture(fixture);

console.log(`Circle facilitator endpoints verified in ${fixturePath}`);
