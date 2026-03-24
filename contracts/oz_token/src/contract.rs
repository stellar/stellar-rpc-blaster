use soroban_sdk::{contract, contractimpl, contracttype, Address, BytesN, Env, MuxedAddress, String, Vec};
use stellar_access::ownable::{self as ownable, Ownable};
use stellar_contract_utils::upgradeable::Upgradeable;
use stellar_macros::only_owner;
use stellar_tokens::fungible::{burnable::FungibleBurnable, Base, FungibleToken};

const TOKEN_DECIMALS: u32 = 7;
const SCHEMA_VERSION_V1: u32 = 1;

#[contracttype]
pub enum DataKey {
    SchemaVersion,
}

#[contract]
pub struct OZTokenContract;

#[contractimpl]
impl OZTokenContract {
    pub fn __constructor(e: &Env, name: String, symbol: String, initial_owner: Address) {
        Base::set_metadata(e, TOKEN_DECIMALS, name, symbol);
        ownable::set_owner(e, &initial_owner);
        e.storage().instance().set(&DataKey::SchemaVersion, &SCHEMA_VERSION_V1);
    }

    #[only_owner]
    pub fn mint_tokens(e: &Env, to: Address, amount: i128) {
        Base::mint(e, &to, amount);
    }

    #[only_owner]
    pub fn mint_batch(e: &Env, recipients: Vec<Address>, amount: i128) {
        for recipient in recipients.iter() {
            Base::mint(e, &recipient, amount);
        }
    }

    pub fn schema_version(e: &Env) -> u32 {
        e.storage().instance().get(&DataKey::SchemaVersion).unwrap_or(0)
    }
}

#[contractimpl]
impl Upgradeable for OZTokenContract {
    #[only_owner]
    fn upgrade(e: &Env, new_wasm_hash: BytesN<32>, _operator: Address) {
        e.deployer().update_current_contract_wasm(new_wasm_hash);
    }
}

#[contractimpl(contracttrait)]
impl FungibleToken for OZTokenContract {
    type ContractType = Base;
}

#[contractimpl(contracttrait)]
impl FungibleBurnable for OZTokenContract {}

#[contractimpl(contracttrait)]
impl Ownable for OZTokenContract {}
