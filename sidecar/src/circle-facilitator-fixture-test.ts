import assert from 'node:assert/strict';
import { validateCircleV1Fixture } from './circle-facilitator-fixture.js';

const validFixture = {
  x402Version: 1,
  facilitatorEndpoints: {
    verify: 'https://gateway-api-testnet.circle.com/gateway/v1/x402/verify',
    settle: 'https://gateway-api-testnet.circle.com/gateway/v1/x402/settle',
  },
};

assert.deepEqual(validateCircleV1Fixture(validFixture), validFixture);

assert.throws(
  () =>
    validateCircleV1Fixture({
      ...validFixture,
      facilitatorEndpoints: {
        ...validFixture.facilitatorEndpoints,
        verify: 'https://x402.org/facilitator/verify',
      },
    }),
  /Expected Circle verify endpoint/,
);

assert.throws(
  () => validateCircleV1Fixture({ ...validFixture, x402Version: 2 }),
  /requires an x402 V1 fixture/,
);

console.log('Circle facilitator fixture validation tests passed');
