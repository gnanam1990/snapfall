import { test } from 'node:test';
import assert from 'node:assert/strict';
import { submitBurnOnce, burnStorageKey } from './cctpBurnGuard';
const tx='0x'+'a'.repeat(64);
const memory=()=>{const m=new Map<string,string>();return {get length(){return m.size;},key:(i:number)=>[...m.keys()][i]??null,getItem:(k:string)=>m.get(k)??null,setItem:(k:string,v:string)=>{m.set(k,v);}};};
test('receipt timeout blocks a second burn',async()=>{const s=memory();let calls=0;await assert.rejects(submitBurnOnce(s,'j',6,async()=>{calls++;throw new Error('timeout');}));await assert.rejects(submitBurnOnce(s,'j',6,async()=>{calls++;return tx;}),/prior transfer/);assert.equal(calls,1);});
test('successful burn is durable and cannot be overwritten',async()=>{const s=memory();const p=await submitBurnOnce(s,'j',6,async()=>tx);assert.deepEqual(p,{domain:6,tx});assert.deepEqual(JSON.parse(s.getItem('j')!),p);await assert.rejects(submitBurnOnce(s,'j',6,async()=>tx),/prior transfer/);});
test('storage failure prevents signing',async()=>{let called=false;await assert.rejects(submitBurnOnce({length:0,key:()=>null,getItem:()=>null,setItem:()=>{throw new Error('denied');}},'j',6,async()=>{called=true;return tx;}));assert.equal(called,false);});
test('in-flight operation blocks concurrent attempt',async()=>{const s=memory();let done!:(v:string)=>void;let calls=0;const first=submitBurnOnce(s,'j',6,()=>{calls++;return new Promise<string>(r=>{done=r;});});await assert.rejects(submitBurnOnce(s,'j',6,async()=>{calls++;return tx;}),/prior transfer/);done(tx);await first;assert.equal(calls,1);});

test('case-variant job URLs share one identity and legacy pending keys block reburn',async()=>{
 assert.equal(burnStorageKey('0xABC'),burnStorageKey('0xabc'));
 const s=memory();s.setItem('snapfall.cctp.0xABC','legacy-pending');let called=false;
 await assert.rejects(submitBurnOnce(s,burnStorageKey('0xabc'),6,async()=>{called=true;return tx;}),/prior transfer/);
 assert.equal(called,false);
});
