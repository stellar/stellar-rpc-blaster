extern crate std;

use soroban_sdk::{testutils::Address as _, vec, Address, Env, String};

use crate::{OZTokenContract, OZTokenContractClient};

fn create_client<'a>(e: &Env, owner: &Address) -> OZTokenContractClient<'a> {
    let address = e.register(
        OZTokenContract,
        (
            String::from_str(e, "BenchToken"),
            String::from_str(e, "BLT"),
            owner.clone(),
        ),
    );
    OZTokenContractClient::new(e, &address)
}

#[test]
fn initialization_sets_metadata_and_owner() {
    let e = Env::default();
    let owner = Address::generate(&e);
    let client = create_client(&e, &owner);

    assert_eq!(client.name(), String::from_str(&e, "BenchToken"));
    assert_eq!(client.symbol(), String::from_str(&e, "BLT"));
    assert_eq!(client.decimals(), 7);
    assert_eq!(client.get_owner(), Some(owner));
    assert_eq!(client.schema_version(), 1);
}

#[test]
fn owner_can_mint() {
    let e = Env::default();
    e.mock_all_auths();

    let owner = Address::generate(&e);
    let alice = Address::generate(&e);
    let client = create_client(&e, &owner);

    client.mint_tokens(&alice, &123);

    assert_eq!(client.balance(&alice), 123);
    assert_eq!(client.total_supply(), 123);
}

#[test]
fn owner_can_mint_batch() {
    let e = Env::default();
    e.mock_all_auths();

    let owner = Address::generate(&e);
    let alice = Address::generate(&e);
    let bob = Address::generate(&e);
    let client = create_client(&e, &owner);

    client.mint_batch(&vec![&e, alice.clone(), bob.clone()], &50);

    assert_eq!(client.balance(&alice), 50);
    assert_eq!(client.balance(&bob), 50);
    assert_eq!(client.total_supply(), 100);
}
